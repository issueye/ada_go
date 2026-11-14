<template>
  <div class="ada-scroll">
    <div class="ada-wrap ada-wrap--page pd-page">
      <div class="pd-head">
        <div class="pd-head__title">
          <h2 class="ada-page-title pd-head__name">{{ plugin?.name || id }}</h2>
          <span class="pd-chip pd-chip--mono">{{ id }}</span>
          <span class="pd-chip pd-chip--mono">{{ plugin?.version ? "v" + plugin.version : "—" }}</span>
          <span class="pd-state" :class="'pd-state--' + (plugin?.statusTone || 'neutral')"><span class="pd-dot"></span>{{ enabledLabel }}</span>
          <span class="pd-tip" data-tip="名称、版本与状态来自插件清单；包内文件只读展示，修改后需重载插件才会生效。" tabindex="0" role="img">i</span>
        </div>
        <div class="ada-section-head__actions pd-head__actions">
          <button v-if="plugin?.enabled" class="pd-btn" type="button" :disabled="busy" @click="toggle">停用</button>
          <button v-else class="pd-btn pd-btn--primary" type="button" :disabled="busy" @click="toggle">启用</button>
          <a v-if="firstPage" class="pd-btn" :href="firstPage" @click.prevent="openFirstPage">打开插件页面</a>
          <a class="pd-btn pd-btn--primary" href="/extensions" @click.prevent="goList">返回插件列表</a>
        </div>
      </div>

      <div v-if="failed" class="ada-empty">
        <span class="ada-empty__mark"><ui-icon name="i-alert" :size="20" /></span>
        <h2 class="ada-empty__title">加载失败</h2>
        <p class="ada-empty__desc">{{ failed }}</p>
        <div class="ada-empty__action"><button class="ada-btn ada-btn--primary ada-btn--md" type="button" @click="load">重试</button></div>
      </div>

      <div v-else-if="plugin" class="pd-root">
        <section class="pd-meta" aria-label="插件信息" id="plugin-info">
          <div class="pd-meta__row">
            <div class="pd-field">
              <span class="pd-field__label">状态</span>
              <span class="pd-field__value" :class="'pd-field__value--' + (plugin.statusTone || 'neutral')">{{ enabledLabel }}</span>
            </div>
            <div class="pd-field">
              <span class="pd-field__label">归属</span>
              <span class="pd-field__value">{{ ownership }}</span>
            </div>
            <div class="pd-field">
              <span class="pd-field__label">信任级别</span>
              <span class="pd-field__value">{{ plugin.trust || "—" }}</span>
            </div>
            <div class="pd-field">
              <span class="pd-field__label">来源</span>
              <span class="pd-field__value">{{ plugin.source || "随包" }}</span>
            </div>
            <div class="pd-field pd-field--grow">
              <span class="pd-field__label">能力</span>
              <span class="pd-caps">
                <span v-if="plugin.tools" class="pd-cap">{{ plugin.tools }} 工具</span>
                <span v-if="capPages" class="pd-cap">{{ capPages }} 页面</span>
                <span v-if="capSlots" class="pd-cap">{{ capSlots }} 插槽</span>
                <span v-if="plugin.hooks" class="pd-cap">{{ plugin.hooks }} 钩子</span>
                <span v-if="plugin.skills" class="pd-cap">{{ plugin.skills }} 技能</span>
                <span v-if="capNav" class="pd-cap">{{ capNav }} 导航</span>
                <span v-if="capDashboard" class="pd-cap pd-cap--ok">dashboard</span>
                <span v-if="!plugin.tools && !capPages && !capSlots && !plugin.hooks && !plugin.skills && !capNav && !capDashboard" class="pd-cap pd-cap--empty">—</span>
              </span>
            </div>
          </div>
          <div class="pd-meta__path">
            <span class="pd-field__label">路径</span>
            <code class="pd-path">{{ plugin.path || "—" }}</code>
            <button type="button" class="pd-link" @click="copyText(plugin.path)">复制</button>
            <span v-if="plugin.apiVersion" class="pd-meta__api">API v{{ plugin.apiVersion }}</span>
          </div>
          <p v-if="plugin.status === 'unused'" class="pd-note">当前产品没有默认启用这个插件；可在插件列表或本页手动启用。</p>
          <p v-if="plugin.description && plugin.description !== plugin.readme" class="pd-note">{{ plugin.description }}</p>
          <p v-if="plugin.error" class="pd-error">{{ plugin.error }}</p>
        </section>

        <div class="pd-main">
          <div class="pd-tree" aria-label="插件目录">
            <div class="pd-tree__head">
              <span class="pd-card__title">插件目录</span>
              <span class="pd-count">{{ fileCount }} 个文件</span>
            </div>
            <ul v-if="plugin.files && plugin.files.length > 0" class="pd-tree__list">
              <li v-for="f in plugin.files" :key="f.path" class="pd-tree__row" :class="f.depthClass || ''">
                <span v-if="f.dir" class="pd-tree__item is-dir" :title="f.path">
                  <span class="pd-tree__bar"></span><span class="pd-tree__bullet"></span>
                  <code class="pd-tree__name">{{ f.path }}</code>
                </span>
                <a v-else class="pd-tree__item" :class="file?.path === f.path ? 'is-active' : ''" href="#" @click.prevent="openFile(f.path)" :title="f.path">
                  <span class="pd-tree__bar"></span><span class="pd-tree__bullet"></span>
                  <code class="pd-tree__name">{{ f.path }}</code>
                  <span class="pd-tree__size">{{ f.sizeLabel }}</span>
                </a>
              </li>
            </ul>
            <p v-else class="pd-tree__empty">这个插件没有可展示的包内文件。</p>
            <p class="pd-tree__hint">点击文件查看内容；Markdown 可切换渲染 / 源码。</p>
          </div>

          <section class="pd-doc" aria-label="文件内容">
            <div class="pd-doc__head">
              <code class="pd-doc__name">{{ file ? file.path : "README.md" }}</code>
              <span v-if="file" class="pd-doc__size">{{ file.sizeLabel }}{{ file.truncated ? " · 已截断" : "" }}</span>
              <div v-if="file && file.markdown" class="pd-seg" role="group" aria-label="视图切换">
                <button class="pd-seg__item" :class="preview ? 'is-active' : ''" type="button" @click="showPreview">预览</button>
                <button class="pd-seg__item" :class="!preview ? 'is-active' : ''" type="button" @click="showSource">源码</button>
              </div>
              <button type="button" class="pd-link pd-doc__copy" @click="copyCurrent">复制</button>
            </div>
            <div class="pd-doc__body">
              <div v-if="file && file.error" class="pd-doc__fallback">
                <p class="pd-doc__error-line">{{ file.error }}</p>
                <span class="pd-doc__fallback-label">无法读取所选文件，以下为插件 README</span>
                <div v-if="readmeNodes" class="markdown-body ada-markdown pd-doc__preview"><ui-markdown :nodes="readmeNodes" /></div>
                <p v-else class="pd-doc__empty">{{ plugin.readme }}</p>
              </div>
              <p v-else-if="file && file.binary" class="pd-doc__empty">这是二进制文件，暂不展示内容。</p>
              <div v-else-if="file && preview && astNodes" class="markdown-body ada-markdown pd-doc__preview"><ui-markdown :nodes="astNodes" /></div>
              <pre v-else-if="file" class="pd-doc__code chroma" data-plugin-file-content><ui-code :text="file.content" :lang="fileLang" /></pre>
              <div v-else-if="plugin.readme">
                <div v-if="readmeNodes" class="markdown-body ada-markdown pd-doc__preview"><ui-markdown :nodes="readmeNodes" /></div>
                <p v-else class="pd-doc__empty">{{ plugin.readme }}</p>
              </div>
              <p v-else class="pd-doc__empty">这个插件没有提供 README。</p>
            </div>
            <div class="pd-doc__foot">
              <span>{{ footLabel }}</span>
              <span>只读</span>
            </div>
          </section>
        </div>

        <div class="pd-foot">
          <div class="pd-foot__left">
            <span class="pd-tip pd-tip--sm" data-tip="第三方插件的入口与能力以插件清单为准；停用后不再贡献工具与页面。" tabindex="0" role="img">i</span>
            <span>第三方插件的入口与能力以插件清单为准；停用后不再贡献工具与页面。</span>
          </div>
          <span class="pd-foot__right">状态来自插件清单</span>
        </div>
      </div>

      <div v-else class="ada-empty">
        <span class="ada-empty__mark"><ui-icon name="i-plug" :size="20" /></span>
        <h2 class="ada-empty__title">没有这个插件</h2>
        <p class="ada-empty__desc">插件可能尚未装配，或已从清单移除。</p>
      </div>
    </div>
  </div>
</template>
<script src="./PluginDetail.js"></script>
