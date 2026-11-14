<template>
  <div class="cm-page">
    <div class="cm-head">
      <div class="cm-title-row">
        <h1 class="cm-title">定时任务</h1>
        <span class="cm-tip cm-tip--below" data-tip="到期后由插件调用 ctx.turn.invoke 触发 Core 轮次（宿主建线程/生成 runId），每个任务复用一个专用线程。任务在 Core 当前工作区执行；任务存于 global.config 的 cron 命名空间，新建或修改后下一次 tick（≤30 秒）生效。" aria-label="定时任务说明" tabindex="0" role="img">!</span>
      </div>
      <div class="cm-actions">
        <button class="cm-btn" type="button" @click="refresh">刷新</button>
      </div>
    </div>

    <div class="cm-summary">
      <span>共 <span class="cm-count">{{ counts.all }}</span> 个任务</span>
      <div class="cm-seg" role="group" aria-label="按状态筛选">
        <button type="button" :class="'cm-seg__item' + (filter === 'all' ? ' is-active' : '')" :aria-pressed="filter === 'all' ? 'true' : 'false'" @click="filter = 'all'">全部 <span>{{ counts.all }}</span></button>
        <button type="button" :class="'cm-seg__item' + (filter === 'active' ? ' is-active' : '')" :aria-pressed="filter === 'active' ? 'true' : 'false'" @click="filter = 'active'">启用 <span>{{ counts.active }}</span></button>
        <button type="button" :class="'cm-seg__item' + (filter === 'paused' ? ' is-active' : '')" :aria-pressed="filter === 'paused' ? 'true' : 'false'" @click="filter = 'paused'">停用 <span>{{ counts.paused }}</span></button>
      </div>
      <span class="cm-summary__time">最后刷新 <span>{{ refreshed || '—' }}</span></span>
    </div>

    <ul v-if="warnings.length > 0" class="cm-notes" aria-label="任务警告">
      <li v-for="(item, i) in warnings" :key="i">{{ item }}</li>
    </ul>

    <section class="cm-card cm-card--form" aria-label="新建任务">
      <div class="cm-card__head">
        <div class="cm-card__title">
          <span>{{ editingId ? '编辑任务' : '新建任务' }}</span>
        </div>
        <span class="cm-card__hint">到期后由插件调用 turn.invoke 触发 Core 轮次，在工作区里新建会话执行</span>
      </div>

      <form class="cm-form" @submit.prevent="submitForm">
        <div class="cm-form__row">
          <label class="cm-field cm-field--grow">
            <span class="cm-field__label">任务名</span>
            <input class="cm-input" type="text" v-model="form.name" placeholder="每天早报" />
          </label>
          <label class="cm-field cm-field--preset">
            <span class="cm-field__label">调度预设</span>
            <select class="cm-input" v-model="form.preset" aria-label="调度预设">
              <option v-for="p in presets" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
          </label>
          <label v-if="form.preset === 'daily'" class="cm-field cm-field--time">
            <span class="cm-field__label">时刻</span>
            <input class="cm-input cm-input--mono" type="text" v-model="form.dailyTime" />
          </label>
          <label v-if="form.preset === 'weekly'" class="cm-field cm-field--time">
            <span class="cm-field__label">星期</span>
            <select class="cm-input" v-model="form.weeklyDay" aria-label="星期">
              <option value="1">周一</option>
              <option value="2">周二</option>
              <option value="3">周三</option>
              <option value="4">周四</option>
              <option value="5">周五</option>
              <option value="6">周六</option>
              <option value="0">周日</option>
            </select>
          </label>
          <label v-if="form.preset === 'weekly'" class="cm-field cm-field--time">
            <span class="cm-field__label">时刻</span>
            <input class="cm-input cm-input--mono" type="text" v-model="form.weeklyTime" />
          </label>
          <span v-if="form.preset !== 'custom'" class="cm-cron-preview">{{ cronPreview }}</span>
        </div>
        <div class="cm-form__row">
          <label v-if="form.preset === 'custom'" class="cm-field cm-field--grow">
            <span class="cm-field__label">cron 表达式（分 时 日 月 周）</span>
            <input class="cm-input cm-input--mono" type="text" v-model="form.cron" placeholder="0 9 * * *" />
          </label>
        </div>
        <div class="cm-form__row">
          <label class="cm-field cm-field--workspace">
            <span class="cm-field__label">工作区</span>
            <select class="cm-input" v-model="form.workspace" aria-label="工作区">
              <option value="">综合工作区（默认）</option>
              <option v-for="ws in workspaceOptions" :key="ws.value" :value="ws.value">{{ ws.label }}</option>
            </select>
          </label>
          <label class="cm-field cm-field--grow">
            <span class="cm-field__label">提示词（发送给 AI 的任务内容）</span>
            <input class="cm-input" type="text" v-model="form.text" placeholder="总结昨天的工作并生成日报" />
          </label>
        </div>
        <div class="cm-form__actions">
          <button class="cm-btn cm-btn--primary" type="submit" :disabled="busy">{{ editingId ? '保存修改' : '创建任务' }}</button>
          <button v-if="editingId" class="cm-btn" type="button" @click="resetForm">取消编辑</button>
          <span v-if="editingId" class="cm-form__editing">正在编辑任务…</span>
        </div>
      </form>
    </section>

    <section class="cm-card" aria-label="任务列表">
      <div class="cm-card__head">
        <div class="cm-card__title">
          <span>任务列表</span>
          <span class="cm-count">{{ visibleTasks.length }}</span>
        </div>
        <span class="cm-card__hint">启停只影响周期调度，「立即运行」随时可触发</span>
      </div>

      <div class="cm-list">
        <div class="cm-row cm-row--head" aria-hidden="true">
          <span>状态</span>
          <span>任务</span>
          <span>工作区</span>
          <span>调度</span>
          <span>下次运行</span>
          <span>上次结果</span>
          <span>操作</span>
        </div>
        <div>
          <div v-if="visibleTasks.length === 0" class="cm-empty">
            <p class="cm-empty__title">{{ counts.all > 0 ? '这个筛选下没有任务' : '还没有定时任务' }}</p>
            <p class="cm-empty__desc">{{ counts.all > 0 ? '切回「全部」查看其它任务。' : '用上面的表单创建第一个 AI 任务。' }}</p>
          </div>
          <div v-for="task in visibleTasks" :key="task.id" class="cm-row" :data-enabled="task.enabled ? 'true' : 'false'">
            <span class="cm-row__state">
              <button class="cm-switch" type="button" role="switch" :data-on="task.enabled ? 'true' : 'false'" :aria-checked="task.enabled ? 'true' : 'false'" :aria-label="(task.enabled ? '停用 ' : '启用 ') + (task.name || task.id)" :title="task.enabled ? '点击停用（停用只停周期调度）' : '点击启用'" @click="onToggle(task.id)"><span class="cm-switch__knob"></span></button>
            </span>
            <div class="cm-row__name">
              <span class="cm-row__name-text">{{ task.name || '' }}</span>
              <span v-if="task.triggerRequested" class="cm-chip cm-chip--warn">待运行</span>
            </div>
            <span class="cm-row__workspace" :title="task.workspace || (workspaceDefault ? '综合工作区（默认：' + workspaceDefault + '）' : '综合工作区（默认）')">{{ task.workspace ? workspaceLabel(task.workspace) : '综合' }}</span>
            <span class="cm-row__cron">{{ task.cron || '—' }}</span>
            <span class="cm-row__time">{{ task.nextRunAt ? formatTime(task.nextRunAt) : '—' }}</span>
            <span class="cm-row__last">
              <span :class="'cm-run cm-run--' + lastRunTone(task)">{{ lastRunTone(task) === 'none' ? '—' : runLabel(lastRunStatus(task)) }}</span>
            </span>
            <div class="cm-row__actions">
              <button class="cm-action cm-action--primary" type="button" title="立即运行一次（不受启停影响；经网关调度层投递）" @click="onTrigger(task.id)">运行</button>
              <button class="cm-action" type="button" @click="onEdit(task.id)">编辑</button>
              <button class="cm-action" type="button" @click="showRuns(task.id)">历史</button>
              <button class="cm-action cm-action--danger" type="button" @click="onRemove(task.id)">删除</button>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section v-if="runsOpen" class="cm-card cm-runs" aria-label="运行历史">
      <div class="cm-card__head">
        <div class="cm-card__title">
          <span>运行历史</span>
          <span class="cm-card__name">{{ runsName }}</span>
        </div>
        <button class="cm-link" type="button" @click="hideRuns">收起</button>
      </div>
      <div class="cm-list">
        <div class="cm-row cm-row--runs cm-row--head" aria-hidden="true">
          <span>触发时刻</span>
          <span>结果</span>
          <span>耗时</span>
          <span>说明</span>
        </div>
        <div>
          <div v-if="runs.length === 0" class="cm-empty">
            <p class="cm-empty__title">还没有运行记录</p>
            <p class="cm-empty__desc">任务到期或点「运行」后这里会出现历史。</p>
          </div>
          <div v-for="(run, ri) in runs" :key="ri" class="cm-row cm-row--runs">
            <span class="cm-row__time">{{ formatTime(run.at) }}</span>
            <span class="cm-row__last">
              <span :class="'cm-run cm-run--' + runTone(run.status)">{{ runLabel(run.status) }}</span>
            </span>
            <span class="cm-row__size">{{ duration(run.durationMs) }}</span>
            <span class="cm-row__detail">{{ runDetail(run) }}</span>
          </div>
        </div>
      </div>
    </section>

    <p class="cm-foot">
      调度为 5 段 cron 表达式（分 时 日 月 周），日与周同时受限时按 cron 惯例取「或」。
      工作区决定任务在哪个项目的会话里运行，切换工作区后任务线程会重建。
    </p>

    <div v-if="toast" class="cm-toast" :data-tone="toast.tone" role="status" aria-live="polite">{{ toast.message }}</div>
  </div>
</template>
<script src="./CronPage.js"></script>
