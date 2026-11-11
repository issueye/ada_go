<template>
  <div class="em-page">
    <div class="em-head">
      <div class="em-title-row">
        <h1 class="em-title">环境变量</h1>
        <span class="em-tip em-tip--below" data-tip="注入插件启动的子进程（ctx.exec、后台任务、stdio MCP）。全局 ~/.adago/env.json 与工作区 .adago/env.json 两层，同名时工作区覆盖全局；改完需重载插件。" aria-label="注入插件启动的子进程（ctx.exec、后台任务、stdio MCP）。全局 ~/.adago/env.json 与工作区 .adago/env.json 两层，同名时工作区覆盖全局；改完需重载插件。" tabindex="0" role="img">!</span>
      </div>
      <div class="em-actions">
        <button class="em-btn" type="button" @click="load">刷新</button>
        <button class="em-btn" type="button" :disabled="reloading" @click="reloadPlugins">{{ reloading ? '重载中…' : '重载插件' }}</button>
      </div>
    </div>

    <section class="em-card" aria-label="环境变量">
      <div class="em-card__head">
        <div class="em-card__title">
          <span>环境变量</span>
          <span class="em-count">{{ counts.all }}</span>
        </div>
        <div class="em-seg em-seg--sm" role="group" aria-label="按范围筛选">
          <button type="button" :class="'em-seg__item' + (filter === 'all' ? ' is-active' : '')" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">全部</button>
          <button type="button" :class="'em-seg__item' + (filter === 'workspace' ? ' is-active' : '')" :aria-pressed="filter === 'workspace' ? 'true' : 'false'" @click="filter = 'workspace'">工作区</button>
          <button type="button" :class="'em-seg__item' + (filter === 'global' ? ' is-active' : '')" :aria-pressed="filter === 'global' ? 'true' : 'false'" @click="filter = 'global'">全局</button>
        </div>
      </div>

      <form class="em-form" @submit.prevent="submit">
        <div class="em-seg" role="group" aria-label="范围">
          <button v-for="scope in scopes" :key="scope.value" type="button" :class="'em-seg__item' + (form.scope === scope.value ? ' is-active' : '')" :aria-pressed="form.scope === scope.value ? 'true' : 'false'" @click="form.scope = scope.value">{{ scope.label }}</button>
        </div>
        <input class="em-input em-mono" type="text" v-model="form.key" placeholder="变量名，如 GITHUB_TOKEN" aria-label="变量名" :aria-invalid="keyError ? 'true' : 'false'" />
        <input class="em-input em-mono" type="text" v-model="form.value" placeholder="值（可为空）" aria-label="值" />
        <button class="em-btn em-btn--primary" type="submit" :disabled="busy || keyError !== ''">添加 / 更新</button>
      </form>
      <p v-if="keyError" class="em-form__error" role="alert">{{ keyError }}</p>

      <div class="em-list">
        <p v-if="error" class="em-error" role="alert">{{ error }}</p><p v-else-if="loading" class="em-empty">正在读取环境变量…</p><div v-else-if="filteredRows.length > 0" v-for="row in filteredRows" :key="row.id" class="em-row">
          <code :class="'em-row__key' + (row.overridden ? ' is-overridden' : '')">{{ row.key }}</code>
          <span :class="'em-row__value em-mono' + (revealed[row.id] || !row.value ? ' is-plain' : '')">{{ row.value ? displayValue(row) : '（空值）' }}</span>
          <span :class="'em-chip em-chip--' + row.scope">{{ scopeLabel(row.scope) }}</span>
          <span v-if="row.overridden" class="em-chip em-chip--warn" title="同名全局变量被工作区覆盖">覆盖全局</span>
          <span class="em-row__actions">
            <button v-if="row.value" class="em-link" type="button" @click="toggleReveal(row)">{{ revealed[row.id] ? '隐藏' : '显示' }}</button>
            <button class="em-link" type="button" @click="copyText(row.value, '变量值')">复制</button>
            <button class="em-link em-link--danger" type="button" @click="removeVar(row)">删除</button>
          </span>
        </div><p v-else-if="counts.all > 0" class="em-empty">当前筛选下没有变量。</p><div v-else class="em-empty em-empty--block">
          <p class="em-empty__title">还没有环境变量</p>
          <p class="em-empty__desc">在上面添加，或直接用命令行写入：</p>
          <div class="em-cmd">
            <code>adag env set GITHUB_TOKEN=***</code>
            <button class="em-link" type="button" @click="copyText('adag env set GITHUB_TOKEN=', '命令')">复制</button>
          </div>
        </div>
      </div>

      <ul v-if="warnings.length > 0" class="em-notes">
        <li v-for="(item, i) in warnings" :key="i">{{ item }}</li>
      </ul>

      <p class="em-foot">值默认打码，仅在本页可见；命名需匹配 <code>^[A-Za-z_][A-Za-z0-9_]*$</code>，<code>ADAG_</code> 前缀保留。</p>
    </section>

    <section class="em-card" aria-label="服务程序目录">
      <div class="em-card__head">
        <div class="em-card__title">
          <span>服务程序目录</span>
          <span class="em-tip em-tip--below" data-tip="把可执行文件放进这些目录，插件子进程的 PATH 会自动前置它们（工作区优先）；新启动的子进程生效，不用写绝对路径。" aria-label="把可执行文件放进这些目录，插件子进程的 PATH 会自动前置它们（工作区优先）；新启动的子进程生效，不用写绝对路径。" tabindex="0" role="img">!</span>
        </div>
        <span class="em-card__hint">放进目录的可执行文件会自动加入插件子进程 PATH</span>
      </div>

      <div class="em-bins">
        <div v-for="item in binPaths" :key="item.scope" class="em-bin">
          <div class="em-bin__top">
            <span :class="'em-chip em-chip--' + item.scope">{{ scopeLabel(item.scope) }}</span>
            <span :class="'em-status' + (item.exists ? '' : ' is-missing')">{{ item.exists ? '已创建' : '未创建' }}</span>
          </div>
          <code class="em-bin__path">{{ item.path }}</code>
          <button class="em-link em-bin__copy" type="button" @click="copyText(item.path, '目录路径')">复制路径</button>
        </div>
        <p v-if="binPaths.length === 0" class="em-empty">还没有可用的服务程序目录。</p>
      </div>

      <div class="em-prog-head">
        <span class="em-prog-head__name">程序</span>
        <span class="em-prog-head__size">大小</span>
        <span class="em-prog-head__time">修改时间</span>
      </div>
      <div class="em-progs">
        <div v-if="binPrograms.length > 0" v-for="item in binPrograms" :key="item.scope + ':' + item.name" class="em-prog">
          <span class="em-prog__cell">
            <code class="em-prog__name">{{ item.name }}</code>
            <span :class="'em-chip em-chip--' + item.scope">{{ scopeLabel(item.scope) }}</span>
          </span>
          <span class="em-prog__size">{{ formatBytes(item.size) }}</span>
          <span class="em-prog__time">{{ formatTime(item.modified) }}</span>
        </div><p v-else class="em-empty">目录里还没有可执行文件，把程序放进去即可。</p>
      </div>
    </section>

    <div v-if="toast" class="em-toast" :data-tone="toast.tone" role="status" aria-live="polite">{{ toast.message }}</div>
  </div>
</template>
<script src="./EnvSettings.js"></script>
