/**
 * 定时任务管理页（official/cron-manager）：/ext/cron-manager/cron
 *
 * 首屏只渲染骨架；数据与操作由 static/cron-manager.js 经本插件 route 完成：
 *   GET  /state            任务列表（含下次运行预览）+ 警告
 *   POST /create           新建任务（name/cron/action）
 *   POST /update           保存任务修改
 *   POST /remove           删除任务
 *   POST /toggle           启停任务
 *   POST /trigger          立即运行（cron 二进制下个 tick 消费，最迟约 5 秒）
 *   GET  /runs?id=         运行历史（最新在前）
 *
 * 任务定义存 ~/.adago/cron.json；调度与执行在独立 cron 二进制：
 *   - 任务只有 AI 任务一种，经网关 /api/cron/fire 送达 core
 *     （thread.create + turn.start），每个任务复用一个专用线程；
 *     无人值守轮次的审批交互按 Core 现有超时过期。HTTP 动作已移除。
 */
import { Select } from "ssr:components";

export default function Page(_props: any) {
  return (
    <div class="cm-page" data-cm-page>
      <div class="cm-head">
        <div class="cm-head__text">
          <h1 class="cm-title">定时任务</h1>
          <p class="cm-sub">
            到期后由独立 cron 二进制把 <code>AI 任务</code> 经网关送达 core（thread.create + turn.start），
            每个任务复用一个专用线程。任务在选定的工作区里执行；不选工作区时落在
            <code>综合工作区</code>（默认工作区）。任务存于 <code>~/.adago/cron.json</code>；
            新建/修改后无需重载，cron 二进制最迟约 5 秒内生效。
          </p>
        </div>
        <div class="cm-head__actions">
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-cm-refresh>
            刷新
          </button>
        </div>
      </div>

      <p class="cm-status" data-cm-status role="status" aria-live="polite"></p>

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
            <input class="cm-input" data-cm-daily-time type="text" name="dailyTime" value="09:00" />
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
            <input class="cm-input" data-cm-weekly-time type="text" name="weeklyTime" value="09:00" />
          </label>
        </div>
        <div class="cm-form__row">
          <label class="cm-field cm-field--grow cm-hidden" data-cm-cron-field>
            <span class="cm-field__label">cron 表达式（分 时 日 月 周）</span>
            <input class="cm-input" data-cm-cron name="cron" placeholder="0 9 * * *" />
          </label>
          <span class="cm-cron-preview" data-cm-cron-preview></span>
        </div>
        <div class="cm-form__row">
          <label class="cm-field cm-field--grow">
            <span class="cm-field__label">工作区</span>
            <Select class="cm-input" data-cm-workspace value="" placeholder="综合工作区（默认）" aria-label="工作区" options={[]} />
          </label>
        </div>
        <div class="cm-form__row">
          <label class="cm-field cm-field--grow">
            <span class="cm-field__label">提示词（发送给 AI 的任务内容）</span>
            <input class="cm-input" data-cm-text name="text" placeholder="总结昨天的工作并生成日报" />
          </label>
        </div>
        <div class="cm-form__actions">
          <button class="ada-btn ada-btn--primary ada-btn--sm" type="submit" data-cm-submit>
            创建任务
          </button>
          <button class="ada-btn ada-btn--ghost ada-btn--sm cm-hidden" type="button" data-cm-cancel>
            取消编辑
          </button>
          <span class="cm-form__editing cm-hidden" data-cm-editing>正在编辑任务…</span>
        </div>
      </form>

      <div class="cm-table-wrap">
        <table class="cm-table">
          <thead>
            <tr>
              <th class="cm-col-enabled">状态</th>
              <th class="cm-col-name">任务名</th>
              <th class="cm-col-workspace">工作区</th>
              <th class="cm-col-cron">调度</th>
              <th class="cm-col-time">下次运行</th>
              <th class="cm-col-time">上次结果</th>
              <th class="cm-col-actions">操作</th>
            </tr>
          </thead>
          <tbody data-cm-rows>
            <tr>
              <td class="cm-empty" colspan="7">正在读取任务…</td>
            </tr>
          </tbody>
        </table>
      </div>

      <section class="cm-runs cm-hidden" data-cm-runs aria-label="运行历史">
        <div class="cm-runs__head">
          <h2 class="cm-runs__title">
            运行历史 · <span data-cm-runs-name></span>
          </h2>
          <button class="ada-btn ada-btn--ghost ada-btn--sm" type="button" data-cm-runs-close>
            收起
          </button>
        </div>
        <div class="cm-table-wrap">
          <table class="cm-table cm-table--runs">
            <thead>
              <tr>
                <th class="cm-col-time">触发时刻</th>
                <th class="cm-col-status">结果</th>
                <th class="cm-col-size">耗时</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody data-cm-runs-rows></tbody>
          </table>
        </div>
      </section>

      <p class="cm-hint">
        工作区决定任务在哪个项目的会话里运行；不选时落在综合工作区（默认工作区）。切换工作区后任务线程会重建。
        AI 任务在无人值守时，需要审批的工具调用按 Core 现有超时（默认 300 秒）自动过期，任务不会卡死；
        「立即运行」不受启停影响（停用只停周期调度），由 cron 二进制在下个 tick（默认 5 秒内）消费。
        调度为 5 段 cron 表达式（分 时 日 月 周），
        日与周同时受限时按 cron 惯例取「或」。
      </p>
    </div>
  );
}
