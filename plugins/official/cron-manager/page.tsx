/**
 * 定时任务管理页（official/cron-manager）：/ext/cron-manager/cron
 *
 * 与 provider-admin / env-manager / mcp-client / token-usage 同一套页面语言：
 * 页头（标题 + ⓘ tooltip + 刷新）→ 汇总条（数量 / 状态筛选分段 / 最后刷新）→
 * 新建任务卡（一行式表单）→ 任务列表卡（行列对齐的卡片化列表）→ 运行历史卡。
 * 一次性反馈走底部 toast（脚本注入 .cm-toast），长说明收进 ⓘ，正文只留数据。
 *
 * 首屏只渲染骨架；数据与操作由 static/cron-manager.js 经本插件 route 完成：
 *   GET  /state            任务列表（含下次运行预览）+ 警告
 *   POST /create           新建任务（name/cron/action）
 *   POST /update           保存任务修改
 *   POST /remove           删除任务
 *   POST /toggle           启停任务
 *   POST /trigger          立即运行（插件直接调用 turn.invoke，约 1 秒内）
 *   GET  /runs?id=         运行历史（最新在前）
 *
 * 任务定义存 global.config 的 cron 命名空间；调度在插件内（agent.every + 5 段 cron 解析）：
 *   - 任务只有 AI 任务一种，由插件调用 ctx.turn.invoke 触发 Core 轮次
 *     （thread.create + turn.start），每个任务复用一个专用线程；
 *     无人值守轮次的审批交互按 Core 现有超时过期。HTTP 动作已移除。
 */
import { Select } from "ssr:components";

export default function Page(_props: any) {
  return (
    <div class="cm-page" data-cm-page>
      <div class="cm-head">
        <div class="cm-title-row">
          <h1 class="cm-title">定时任务</h1>
          <span
            class="cm-tip cm-tip--below"
            data-tip="到期后由插件调用 ctx.turn.invoke 触发 Core 轮次（宿主建线程/生成 runId），每个任务复用一个专用线程。任务在 Core 当前工作区执行；任务存于 global.config 的 cron 命名空间，新建或修改后下一次 tick（≤30 秒）生效。"
            aria-label="定时任务说明"
            tabindex="0"
            role="img"
          >!</span>
        </div>
        <div class="cm-actions">
          <button class="cm-btn" type="button" data-cm-refresh>刷新</button>
        </div>
      </div>

      <div class="cm-summary">
        <span>共 <span class="cm-count" data-cm-count="all">0</span> 个任务</span>
        <div class="cm-seg" role="group" aria-label="按状态筛选">
          <button class="cm-seg__item is-active" type="button" data-cm-filter="all" aria-pressed="true">
            全部 <span data-cm-count-all>0</span>
          </button>
          <button class="cm-seg__item" type="button" data-cm-filter="active" aria-pressed="false">
            启用 <span data-cm-count-active>0</span>
          </button>
          <button class="cm-seg__item" type="button" data-cm-filter="paused" aria-pressed="false">
            停用 <span data-cm-count-paused>0</span>
          </button>
        </div>
        <span class="cm-summary__time">最后刷新 <span data-cm-refreshed>—</span></span>
      </div>

      <ul class="cm-notes cm-hidden" data-cm-notes aria-label="任务警告"></ul>

      <section class="cm-card cm-card--form" aria-label="新建任务">
        <div class="cm-card__head">
          <div class="cm-card__title">
            <span data-cm-form-title>新建任务</span>
          </div>
          <span class="cm-card__hint">到期后由插件调用 turn.invoke 触发 Core 轮次，在工作区里新建会话执行</span>
        </div>

        <form class="cm-form" data-cm-form>
          <div class="cm-form__row">
            <label class="cm-field cm-field--grow">
              <span class="cm-field__label">任务名</span>
              <input class="cm-input" data-cm-name name="name" placeholder="每天早报" />
            </label>
            <label class="cm-field cm-field--preset">
              <span class="cm-field__label">调度预设</span>
              <Select class="cm-input" data-cm-preset value="*/5 * * * *" aria-label="调度预设" options={[
                { value: "*/5 * * * *", label: "每 5 分钟" },
                { value: "*/15 * * * *", label: "每 15 分钟" },
                { value: "0 * * * *", label: "每小时" },
                { value: "daily", label: "每天（指定时刻）" },
                { value: "weekly", label: "每周（指定星期与时刻）" },
                { value: "custom", label: "自定义 cron 表达式" },
              ]} />
            </label>
            <label class="cm-field cm-field--time cm-hidden" data-cm-daily-field>
              <span class="cm-field__label">时刻</span>
              <input class="cm-input cm-input--mono" data-cm-daily-time type="text" name="dailyTime" value="09:00" />
            </label>
            <label class="cm-field cm-field--time cm-hidden" data-cm-weekly-field>
              <span class="cm-field__label">星期</span>
              <Select class="cm-input" data-cm-weekly-day value="1" aria-label="星期" options={[
                { value: "1", label: "周一" },
                { value: "2", label: "周二" },
                { value: "3", label: "周三" },
                { value: "4", label: "周四" },
                { value: "5", label: "周五" },
                { value: "6", label: "周六" },
                { value: "0", label: "周日" },
              ]} />
            </label>
            <label class="cm-field cm-field--time cm-hidden" data-cm-weekly-field>
              <span class="cm-field__label">时刻</span>
              <input class="cm-input cm-input--mono" data-cm-weekly-time type="text" name="weeklyTime" value="09:00" />
            </label>
            <span class="cm-cron-preview" data-cm-cron-preview></span>
          </div>
          <div class="cm-form__row">
            <label class="cm-field cm-field--grow cm-hidden" data-cm-cron-field>
              <span class="cm-field__label">cron 表达式（分 时 日 月 周）</span>
              <input class="cm-input cm-input--mono" data-cm-cron name="cron" placeholder="0 9 * * *" />
            </label>
          </div>
          <div class="cm-form__row">
            <label class="cm-field cm-field--workspace">
              <span class="cm-field__label">工作区</span>
              <Select class="cm-input" data-cm-workspace value="" placeholder="综合工作区（默认）" aria-label="工作区" options={[]} />
            </label>
            <label class="cm-field cm-field--grow">
              <span class="cm-field__label">提示词（发送给 AI 的任务内容）</span>
              <input class="cm-input" data-cm-text name="text" placeholder="总结昨天的工作并生成日报" />
            </label>
          </div>
          <div class="cm-form__actions">
            <button class="cm-btn cm-btn--primary" type="submit" data-cm-submit>
              创建任务
            </button>
            <button class="cm-btn cm-hidden" type="button" data-cm-cancel>
              取消编辑
            </button>
            <span class="cm-form__editing cm-hidden" data-cm-editing>正在编辑任务…</span>
          </div>
        </form>
      </section>

      <section class="cm-card" aria-label="任务列表">
        <div class="cm-card__head">
          <div class="cm-card__title">
            <span>任务列表</span>
            <span class="cm-count" data-cm-list-count>0</span>
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
          <div data-cm-rows>
            <div class="cm-empty">正在读取任务…</div>
          </div>
        </div>
      </section>

      <section class="cm-card cm-runs cm-hidden" data-cm-runs aria-label="运行历史">
        <div class="cm-card__head">
          <div class="cm-card__title">
            <span>运行历史</span>
            <span class="cm-card__name" data-cm-runs-name></span>
          </div>
          <button class="cm-link" type="button" data-cm-runs-close>收起</button>
        </div>
        <div class="cm-list">
          <div class="cm-row cm-row--runs cm-row--head" aria-hidden="true">
            <span>触发时刻</span>
            <span>结果</span>
            <span>耗时</span>
            <span>说明</span>
          </div>
          <div data-cm-runs-rows></div>
        </div>
      </section>

      <p class="cm-foot">
        调度为 5 段 cron 表达式（分 时 日 月 周），日与周同时受限时按 cron 惯例取「或」。
        工作区决定任务在哪个项目的会话里运行，切换工作区后任务线程会重建。
      </p>
    </div>
  );
}
