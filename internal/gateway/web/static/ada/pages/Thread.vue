<template>
  <div class="ada-corner-dock" id="chat-corner" aria-label="会话状态卡"><ui-slot name="chat.corner" :thread-id="threadId" /></div>

  <div class="ada-chat-viewport">
    <div class="ada-scroll" id="chat-scroll" @scroll="onScroll">
      <div class="ada-wrap" :class="threadId === '' ? 'ada-wrap--newtask' : ''">

        <div v-if="threadId !== ''" class="ada-host">
          <div class="ada-transcript" id="transcript" :data-thread-id="threadId" :data-run-status="run.status">
            <a v-if="hasOlder" class="ada-history-more" href="#" @click.prevent="loadOlder">加载更早消息</a>
            <div v-if="loadFailed" class="ada-empty">
              <span class="ada-empty__mark"><ui-icon name="i-alert" :size="20" /></span>
              <h2 class="ada-empty__title">暂时没有连上 agent</h2>
              <p class="ada-empty__desc">{{ loadFailed }}。请确认桌面端或网关仍在运行，然后重试。</p>
              <div class="ada-empty__action"><button class="ada-btn ada-btn--primary ada-btn--md" type="button" @click="refreshAll">重新连接</button></div>
            </div>
            <div v-else-if="rows.length === 0 && !liveActive" class="ada-empty">
              <span class="ada-empty__mark"><ui-icon name="i-chat" :size="20" /></span>
              <h2 class="ada-empty__title">这个会话还没有消息</h2>
              <p class="ada-empty__desc">在下方输入内容并发送，agent 的回复会出现在这里。</p>
            </div>
            <div v-for="row in rows" :key="row.key" class="ada-host">
              <div v-if="row.kind === 'user'" class="ada-msg ada-msg--user" :data-item-key="row.key">
                <div class="ada-msg__group">
                  <div class="ada-bubble"><div v-for="(p, pi) in row.parts" :key="row.key + ':' + pi" class="ada-host"><details v-if="p.type === 'thinking'" class="ada-disclosure" data-kind="thinking" data-state="closed"><summary class="ada-disclosure__summary"><span class="ada-disclosure__icon"><ui-icon name="i-bulb" :size="14" /></span><span class="ada-disclosure__caret"><ui-icon name="i-chevron" :size="12" /></span><span class="ada-disclosure__label">思考</span></summary><div class="ada-disclosure__rail"><div class="ada-thinking">{{ p.text }}</div></div></details><button v-else-if="p.type === 'image' && imageHas(p)" class="ada-msg__image" type="button" data-image-viewer="1" :title="'图片 ' + (pi + 1)" :aria-label="'查看图片 ' + (pi + 1)" :style="imageStyle(p)" @click="openImage(row, pi)"><span class="ada-visually-hidden">图片</span></button><span v-else-if="p.type === 'image'" class="ada-msg__images">[图片]</span><div v-else class="part-text">{{ p.text }}</div></div></div>
                  <details v-if="row.canEdit" class="ada-msg__edit" data-msg-edit>
                    <summary class="ada-msg__edit-trigger" @click="startEdit(row)">编辑并重发</summary>
                    <div class="ada-msg__edit-form">
                      <textarea class="ada-input ada-msg__edit-text" rows="3" maxlength="65536" aria-label="编辑消息" v-model="editText"></textarea>
                      <p class="ada-msg__edit-hint">重新发送后，这条消息及其之后的所有对话会被舍弃；旧分支仍保留在存储里，不删除。原消息的图片会保留。</p>
                      <div class="ada-msg__edit-actions">
                        <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="cancelEdit">取消</button>
                        <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" :disabled="sending" @click="resend(row)">重新发送</button>
                      </div>
                    </div>
                  </details>
                </div>
              </div>
              <div v-else-if="row.kind === 'assistant'" class="ada-msg ada-msg--assistant" :data-item-key="row.key">
                <span class="ada-avatar" title="助手"><ui-icon name="i-bot" :size="14" /></span>
                <div class="ada-msg__body markdown-body ada-markdown"><div v-for="(p, pi) in row.parts" :key="row.key + ':' + pi" class="ada-host"><details v-if="p.type === 'thinking'" class="ada-disclosure" data-kind="thinking" data-state="closed"><summary class="ada-disclosure__summary"><span class="ada-disclosure__icon"><ui-icon name="i-bulb" :size="14" /></span><span class="ada-disclosure__caret"><ui-icon name="i-chevron" :size="12" /></span><span class="ada-disclosure__label">思考</span></summary><div class="ada-disclosure__rail"><div class="ada-thinking">{{ p.text }}</div></div></details><button v-else-if="p.type === 'image' && imageHas(p)" class="ada-msg__image ada-msg__image--inline" type="button" data-image-viewer="1" :title="'图片 ' + (pi + 1)" :aria-label="'查看图片 ' + (pi + 1)" :style="imageStyle(p)" @click="openImage(row, pi)"><span class="ada-visually-hidden">图片</span></button><span v-else-if="p.type === 'image'" class="ada-msg__images">[图片]</span><ui-markdown v-else :nodes="p.nodes" :text="p.text" /></div></div>
              </div>
              <details v-else-if="row.kind === 'tool'" class="ada-disclosure" data-kind="tool" data-state="closed" :data-key="row.tool.callId" :data-item-key="row.key" :data-phase="toolPhase(row.tool)">
                <summary class="ada-disclosure__summary">
                  <span class="ada-disclosure__icon"><ui-icon :name="toolIconName(row.tool)" :size="14" /></span>
                  <span class="ada-disclosure__caret"><ui-icon name="i-chevron" :size="12" /></span>
                  <span class="ada-disclosure__label">{{ row.tool.name }}</span>
                  <span v-if="row.tool.argsHint" class="ada-disclosure__detail ada-disclosure__detail--args" :title="row.tool.argsHint">{{ row.tool.argsHint }}</span>
                  <span v-if="row.tool.statusLabel" class="ada-badge" :class="'ada-badge--' + toolTone(row.tool.status)">{{ row.tool.statusLabel }}</span>
                  <span v-if="row.tool.durationMs" class="ada-disclosure__meta">{{ row.tool.durationMs }} ms</span>
                  <span v-if="row.tool.truncated" class="ada-badge ada-badge--warn">已截断</span>
                </summary>
                <div class="ada-disclosure__rail">
                  <p v-if="row.tool.reason" class="ada-notice__row">原因：{{ row.tool.reason }}</p>
                  <div v-if="row.tool.output" class="ada-panel ada-tool-output" data-expanded="shown" data-body="text">
                    <div class="ada-panel__head">
                      <span class="ada-panel__title">执行输出</span>
                      <span class="ada-tool-output__meta">共 {{ lineCount(row.tool.output) }} 行</span>
                      <button class="ada-panel__copy" type="button" @click="copyText(row.tool.output)">复制</button>
                    </div>
                    <div class="ada-panel__body ada-tool-output__scroll"><pre class="ada-tool-output__preview">{{ row.tool.output }}</pre></div>
                  </div>
                  <div v-if="row.tool.args" class="ada-panel">
                    <div class="ada-panel__head"><span class="ada-panel__title">输入参数</span><button class="ada-panel__copy" type="button" @click="copyText(row.tool.args)">复制</button></div>
                    <pre class="ada-panel__body">{{ row.tool.args }}</pre>
                  </div>
                  <div v-if="row.tool.details" class="ada-panel">
                    <div class="ada-panel__head"><span class="ada-panel__title">详情</span><button class="ada-panel__copy" type="button" @click="copyText(row.tool.details)">复制</button></div>
                    <pre class="ada-panel__body">{{ row.tool.details }}</pre>
                  </div>
                  <div v-if="!row.tool.output && !row.tool.args && !row.tool.details" class="ada-panel"><div class="ada-panel__body"><p>这个工具没有返回可展示的内容。</p></div></div>
                </div>
              </details>
              <div v-else-if="row.kind === 'notice'" class="ada-notice" :class="'ada-notice--' + noticeTone(row.level)"><div class="ada-notice__row"><ui-icon name="i-alert" :size="14" /><span>{{ row.text }}</span></div></div>
              <div v-else class="ada-msg ada-msg--plain" :data-item-key="row.key"><div class="ada-plain">{{ row.text }}</div></div>
            </div>

            <div v-if="liveActive" class="ada-msg ada-msg--assistant live-msg" :data-message-id="live.messageId">
              <span class="ada-avatar" aria-hidden="true"><ui-icon name="i-bot" :size="14" /></span>
              <div class="ada-msg__body markdown-body ada-markdown">
                <details v-if="live.thinking !== ''" class="ada-disclosure" data-kind="thinking" data-state="open" open>
                  <summary class="ada-disclosure__summary"><span class="ada-disclosure__icon"><ui-icon name="i-bulb" :size="14" /></span><span class="ada-disclosure__caret"><ui-icon name="i-chevron" :size="12" /></span><span class="ada-disclosure__label">思考</span></summary>
                  <div class="ada-disclosure__rail"><div class="ada-thinking">{{ live.thinking }}</div></div>
                </details>
                <ui-markdown :nodes="live.nodes" :text="live.text" />
              </div>
            </div>

            <ui-slot name="transcript.after" :thread-id="threadId" />
          </div>

          <div v-if="statusText !== ''" class="live-status" role="status" aria-live="polite" :data-level="statusLevel">
            <span>{{ statusText }}</span>
            <button v-if="statusRetry" class="ada-live-retry" type="button" @click="reconnect">重新连接</button>
          </div>

          <div v-if="run.errorLabel !== ''" class="ada-run-error" role="alert">
            <span class="ada-run-error__icon"><ui-icon name="i-alert" :size="16" /></span>
            <div class="ada-run-error__body">
              <strong class="ada-run-error__title">{{ runErrorTitle }}</strong>
              <p class="ada-run-error__detail">{{ run.errorLabel }}</p>
              <p v-if="run.errorDetail !== ''" class="ada-run-error__reason">{{ run.errorDetail }}</p>
              <span v-if="run.errorCode !== 0" class="ada-run-error__code">错误码 {{ run.errorCode }}</span>
            </div>
          </div>
        </div>

        <div v-else class="ada-host">
          <div class="ada-newtask">
            <div class="ada-newtask__inner">
              <span class="ada-newtask__mark"><span class="ada-newtask__logo ada-logo-mask" :style="logoStyle" role="img" aria-label="logo"></span></span>
              <h2 class="ada-newtask__greeting">{{ greeting }}</h2>
              <p class="ada-newtask__desc">描述你要完成的任务，agent 会在选定的工作区里开始工作。</p>
              <form class="ada-newtask__form" @submit.prevent="send">
                <div class="ada-composer__card ada-newtask__card">
                  <div class="ada-composer__thumbs" :hidden="thumbs.length === 0">
                    <span v-for="(t, i) in thumbs" :key="t.key" class="ada-composer__thumb">
                      <span class="ada-composer__thumb-img" :style="t.style" role="img" :aria-label="t.name"></span>
                      <button class="ada-composer__thumb-remove" type="button" :aria-label="'移除图片 ' + (i + 1)" @click="removeImage(i)">×</button>
                    </span>
                  </div>
                  <textarea class="ada-composer__input" name="text" rows="2" maxlength="65536" :placeholder="'向 ' + productName + ' 提问，或描述一个任务…'" aria-label="任务描述" v-model="draft" @paste="onPaste" @dragenter="onDragEnter" @dragover.prevent="noop" @dragleave="onDragLeave" @drop.prevent="onDrop"></textarea>
                  <div class="ada-composer__bar">
                    <div class="ada-composer__group ada-composer__group--left">
                      <label class="ada-composer__attach" :for="imageInputID" title="添加图片" aria-label="添加图片"><ui-icon name="i-image" :size="14" /></label>
                      <button v-if="filePickerReady" class="ada-tool ada-tool--workspace" type="button" :title="'选择工作区：' + workspacePath" @click="openWorkspacePicker">
                        <ui-icon name="i-folder" :size="14" /><span class="ada-tool__label">{{ wsLabel }}</span><ui-icon name="i-chevron-down" :size="12" />
                      </button>
                      <select class="ada-input ada-newtask__ws-native" aria-label="工作区" v-model="wsSelected">
                        <option v-for="w in workspaces" :key="w.path" :value="w.path">{{ w.name }} — {{ w.path }}</option>
                      </select>
                      <div v-if="models.length > 0" class="ada-select ada-model-picker">
                        <button class="ada-select__trigger ada-model-picker__trigger" type="button" :aria-expanded="modelOpen ? 'true' : 'false'" title="选择当前模型" @click="modelOpen = !modelOpen">
                          <span class="ada-model-picker__icon"><ui-icon name="i-sparkles" :size="14" /></span>
                          <span class="ada-select__label ada-model-picker__label">{{ currentModelLabel }}</span>
                          <span class="ada-select__caret"><ui-icon name="i-chevron-down" :size="12" /></span>
                        </button>
                        <div class="ada-select__menu ada-model-picker__menu" :hidden="!modelOpen">
                          <p class="ada-model-picker__group">模型</p>
                          <div v-for="m in models" :key="m.value" class="ada-host">
                            <button class="ada-select__option ada-model-picker__option" :class="m.current ? 'is-selected' : ''" type="button" :aria-selected="m.current ? 'true' : 'false'" :aria-haspopup="m.thinkingLevels.length > 0 ? 'menu' : 'false'" @click="chooseModel(m, $event)">
                              <span class="ada-model-picker__name">{{ m.label }}</span>
                              <span class="ada-model-picker__provider">{{ m.providerId }}</span>
                              <span v-if="m.thinkingLevels.length > 0" class="ada-model-picker__chevron"><ui-icon name="i-chevron" :size="12" /></span>
                            </button>
                          </div>
                        </div>
                      </div>
                      <ui-slot name="composer.left" :thread-id="threadId" />
                    </div>
                    <div class="ada-composer__group ada-composer__group--right">
                      <ui-slot name="composer.right" :thread-id="threadId" />
                      <button class="ada-send" type="submit" aria-label="开始任务" title="开始任务"><ui-icon name="i-send" :size="14" /></button>
                    </div>
                  </div>
                </div>
              </form>
            </div>
          </div>
        </div>

      </div>

      <button v-if="threadId !== ''" class="ada-scroll-bottom" id="scroll-bottom" type="button" :hidden="!showScrollBottom" title="回到最新" @click="jumpToBottom">
        <ui-icon name="i-chevron-down" :size="13" /><span>回到最新</span>
      </button>
    </div>

    <div v-if="threadId !== ''" class="ada-float-dock" id="composer-above" aria-label="插件悬浮区"><ui-slot name="composer.above" :thread-id="threadId" /></div>
    <div v-if="threadId !== ''" class="ada-run-status-dock" id="run-status">
      <span v-if="runDockVisible" class="ada-run-status" :data-run-state="run.status" :data-run-started-at="run.startedAt" role="status" aria-live="polite" :aria-label="runStatusText">
        <span class="ada-run-status__mark" aria-hidden="true"><span class="ada-run-eq"><span class="ada-run-eq__bar"></span><span class="ada-run-eq__bar"></span><span class="ada-run-eq__bar"></span><span class="ada-run-eq__bar"></span></span></span>
        <span class="ada-run-status__text" aria-hidden="true">
          <span>{{ runStatusText }}</span><span class="ada-run-status__sep">，</span><span>用时 <span>{{ elapsedText }}</span></span>
          <span class="ada-run-status__dots"><span>.</span><span>.</span><span>.</span></span>
        </span>
      </span>
    </div>

    <div v-if="threadId !== ''" class="ada-trays" id="ada-trays" role="region" aria-live="polite" aria-label="待处理事项">
      <ui-slot name="thread.tray" :thread-id="threadId" />
      <div v-if="trayInteractions.length > 0" class="ada-tray ada-tray--approval" aria-label="待处理交互">
        <div class="ada-tray__head"><span class="ada-tray__icon"><ui-icon name="i-alert" :size="14" /></span><span class="ada-tray__title">待答复 · {{ trayInteractions.length }} 条</span></div>
        <div v-for="x in trayInteractions" :key="x.id" class="ada-host">
          <div class="ada-tray__row" :class="x.kind === 'form' ? 'ada-tray__row--form' : ''">
            <div class="ada-tray__text"><strong>{{ x.title }}</strong><div v-if="x.body !== ''" class="ada-host"> — {{ x.body }}</div></div>
            <div v-if="x.kind === 'form'" class="ada-tray__form ask-wizard">
              <div class="ask-wizard__bar"><span class="ask-wizard__counter">第 <span>{{ wizardStep(x) + 1 }}</span> / {{ x.questions.length }} 题</span></div>
              <div v-for="(q, qi) in x.questions" :key="x.id + ':' + qi" class="ada-host">
                <div class="ask-wizard__step" :hidden="wizardStep(x) !== qi">
                  <p class="ask-wizard__question">{{ q.question }}</p>
                  <select v-if="q.kind === 'select' && q.options.length > 0" class="ada-input ask-wizard__select" :aria-label="'回答：' + q.question" v-model="wizardAnswers[x.id][qi]">
                    <option v-for="opt in q.options" :key="opt" :value="opt">{{ opt }}</option>
                  </select>
                  <div v-else-if="q.kind === 'confirm'" class="ask-wizard__choices">
                    <label class="ask-wizard__choice"><input type="radio" :name="'q' + qi" value="yes" :checked="wizardAnswers[x.id][qi] === 'yes'" @change="wizardAnswers[x.id][qi] = 'yes'"><span>是</span></label>
                    <label class="ask-wizard__choice"><input type="radio" :name="'q' + qi" value="no" :checked="wizardAnswers[x.id][qi] === 'no'" @change="wizardAnswers[x.id][qi] = 'no'"><span>否</span></label>
                  </div>
                  <input v-else class="ada-input ask-wizard__input" type="text" :aria-label="'回答：' + q.question" v-model="wizardAnswers[x.id][qi]">
                </div>
              </div>
              <div class="ask-wizard__nav">
                <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" :disabled="wizardStep(x) === 0" @click="wizardPrev(x)">上一题</button>
                <button v-if="wizardStep(x) < x.questions.length - 1" class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="wizardNext(x)">下一题</button>
                <button v-else class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="respond(x, 'accept')">提交</button>
                <button class="ada-btn ada-btn--ghost ada-btn--sm" type="button" @click="respond(x, 'cancel')">取消</button>
              </div>
            </div>
            <div v-else class="ada-tray__form">
              <select v-if="x.kind === 'select' && x.options.length > 0" class="ada-input ada-tray__select" aria-label="选择" v-model="trayValues[x.id]">
                <option v-for="opt in x.options" :key="opt" :value="opt">{{ opt }}</option>
              </select>
              <input v-else-if="x.kind === 'input'" class="ada-input" type="text" aria-label="回答" v-model="trayValues[x.id]">
              <div v-if="x.kind === 'confirm'" class="ada-host">
                <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="respond(x, 'deny')">拒绝</button>
                <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="respond(x, 'accept')">接受</button>
              </div>
              <div v-else class="ada-host">
                <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="respond(x, 'accept')">提交</button>
                <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="respond(x, 'deny')">拒绝</button>
              </div>
              <button class="ada-btn ada-btn--ghost ada-btn--sm" type="button" @click="respond(x, 'cancel')">取消</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <form v-if="threadId !== ''" class="ada-composer" :class="dragging ? 'is-drop-target' : ''" @submit.prevent="send">
      <div class="ada-outbox" :hidden="outbox.length === 0" role="region" aria-live="polite" aria-label="待发送消息">
        <div class="ada-outbox__head"><span>待发送 · {{ outbox.length }}</span></div>
        <div v-for="item in outbox" :key="item.id" class="ada-outbox__item">
          <p class="ada-outbox__text" :title="item.text">{{ item.text }}</p>
          <div class="ada-outbox__actions">
            <button class="ada-outbox__action" type="button" title="编辑" aria-label="编辑" @click="editOutbox(item.id)"><ui-icon name="i-edit" :size="14" /></button>
            <button class="ada-outbox__action ada-outbox__action--primary" type="button" title="立即发送" aria-label="立即发送" @click="sendOutbox(item.id, false)"><ui-icon name="i-send" :size="14" /></button>
            <button class="ada-outbox__action ada-outbox__action--danger" type="button" title="删除" aria-label="删除" @click="deleteOutbox(item.id)"><ui-icon name="i-trash" :size="14" /></button>
          </div>
        </div>
      </div>
      <div class="ada-composer__card">
        <div class="ada-composer__thumbs" :hidden="thumbs.length === 0">
          <span v-for="(t, i) in thumbs" :key="t.key" class="ada-composer__thumb">
            <span class="ada-composer__thumb-img" :style="t.style" role="img" :aria-label="t.name"></span>
            <button class="ada-composer__thumb-remove" type="button" :aria-label="'移除图片 ' + (i + 1)" @click="removeImage(i)">×</button>
          </span>
        </div>
        <textarea class="ada-composer__input" name="text" rows="2" maxlength="65536" :placeholder="'向 ' + productName + ' 提问，或指派一个任务…'" aria-label="消息输入框" v-model="draft" @paste="onPaste" @dragenter="onDragEnter" @dragover.prevent="noop" @dragleave="onDragLeave" @drop.prevent="onDrop"></textarea>
        <div class="ada-composer__bar">
          <div class="ada-composer__group ada-composer__group--left">
            <label class="ada-composer__attach" :for="imageInputID" title="添加图片" aria-label="添加图片"><ui-icon name="i-image" :size="14" /></label>
            <details class="ada-plus">
              <summary class="ada-plus__button" title="更多操作" aria-label="更多操作"><ui-icon name="i-plus" :size="14" /></summary>
              <div class="ada-plus__panel">
                <a class="ada-plus__item" href="/threads" @click.prevent="goNewTask"><ui-icon name="i-plus" :size="14" /><span>新建任务</span></a>
              </div>
            </details>
            <span class="ada-tool ada-tool--workspace" :title="workspacePath"><ui-icon name="i-git-branch" :size="14" /><span class="ada-tool__label">{{ wsLabel }}</span></span>
            <div v-if="models.length > 0" class="ada-select ada-model-picker">
              <button class="ada-select__trigger ada-model-picker__trigger" type="button" :aria-expanded="modelOpen ? 'true' : 'false'" title="选择当前模型" @click="modelOpen = !modelOpen">
                <span class="ada-model-picker__icon"><ui-icon name="i-sparkles" :size="14" /></span>
                <span class="ada-select__label ada-model-picker__label">{{ currentModelLabel }}</span>
                <span class="ada-select__caret"><ui-icon name="i-chevron-down" :size="12" /></span>
              </button>
              <div class="ada-select__menu ada-model-picker__menu" :hidden="!modelOpen">
                <p class="ada-model-picker__group">模型</p>
                <div v-for="m in models" :key="m.value" class="ada-host">
                  <button class="ada-select__option ada-model-picker__option" :class="m.current ? 'is-selected' : ''" type="button" :aria-selected="m.current ? 'true' : 'false'" :aria-haspopup="m.thinkingLevels.length > 0 ? 'menu' : 'false'" @click="chooseModel(m, $event)">
                    <span class="ada-model-picker__name">{{ m.label }}</span>
                    <span class="ada-model-picker__provider">{{ m.providerId }}</span>
                    <span v-if="m.thinkingLevels.length > 0" class="ada-model-picker__chevron"><ui-icon name="i-chevron" :size="12" /></span>
                  </button>
                </div>
              </div>
            </div>
            <ui-slot name="composer.left" :thread-id="threadId" />
          </div>
          <div class="ada-composer__group ada-composer__group--right">
            <ui-slot name="composer.right" :thread-id="threadId" />
            <button class="ada-send" type="submit" :aria-label="run.active ? '加入待发送队列' : '发送'" :title="run.active ? '加入待发送队列，可编辑或立即发送' : '发送（Enter）'"><ui-icon name="i-send" :size="14" /></button>
          </div>
        </div>
      </div>
      <div id="composer-aux"><ui-slot name="composer.footer" :thread-id="threadId" /></div>
    </form>
  </div>
</template>
<style scoped>
/* 会话页的少量 SPA 补充（其余 DOM/CSS 与 HEAD 同形，样式来自 a_da 与 app-local）：
   - 图片用 background-image 呈现（白名单没有 img；img-src CSP 放行 data:）；
   - 运行状态标记用单帧均衡器 mask + 呼吸动画（HEAD 的逐柱动画依赖 SVG rect）。 */
.ada-composer__thumb-img {
	display: inline-block;
	width: 56px;
	height: 56px;
	border-radius: var(--ada-radius, 6px);
	background-color: var(--ph-subtle, canvas);
	-webkit-mask-image: none;
	mask-image: none;
	background-size: cover;
	background-position: center;
}
.ada-msg__image {
	display: inline-block;
	inline-size: 200px;
	block-size: 140px;
	max-inline-size: 100%;
	padding: 0;
	border: 1px solid var(--ph-line, transparent);
	border-radius: var(--ada-radius, 6px);
	background-color: var(--ph-subtle, canvas);
	background-size: cover;
	background-position: center;
	cursor: zoom-in;
}
.ada-visually-hidden {
	position: absolute;
	width: 1px;
	height: 1px;
	overflow: hidden;
	clip: rect(0 0 0 0);
	white-space: nowrap;
}
.ada-newtask__logo.ada-logo-mask {
	display: inline-block;
	width: 40px;
	height: 40px;
	background-color: currentcolor;
	-webkit-mask-image: url("/static/logo.png");
	mask-image: url("/static/logo.png");
	-webkit-mask-size: contain;
	mask-size: contain;
	-webkit-mask-repeat: no-repeat;
	mask-repeat: no-repeat;
	-webkit-mask-position: center;
	mask-position: center;
}
/* 运行状态标记：四根竖柱逐柱错峰起伏（HEAD chat.html.tmpl 的 4 个 SVG rect，
   高度比 6/14/10/4、居中、0/120/240/360ms 错峰）。白名单不收 svg，这里用
   span + CSS keyframes 复刻同一形态（transform-box 语义由 transform-origin 覆盖）。 */
@keyframes ada-run-eq-pulse {
	0%, 100% { opacity: 0.4; transform: scaleY(0.35); }
	45% { opacity: 1; transform: scaleY(1); }
}
.ada-run-eq {
	display: inline-flex;
	width: 14px;
	height: 14px;
	align-items: center;
	justify-content: center;
	/* HEAD 的 4 个 rect 宽 2.4、间距 2.4（24 单位 viewBox 渲染成 14px）：
     换算到 14px 是宽 1.4px / 间距 1.4px，整组 9.8px 居中。 */
	gap: 1.4px;
}
.ada-run-eq__bar {
	display: block;
	width: 1.4px;
	flex: 0 0 1.4px;
	border-radius: 0.7px;
	background: currentcolor;
	transform-origin: center;
	animation: ada-run-eq-pulse 900ms ease-in-out infinite;
}
/* 柱高照 HEAD 的 rect（6/14/10/4 单位 → 3.5/8.2/5.8/2.3px），错峰 0/150/300/450ms。 */
.ada-run-eq__bar:nth-child(1) { height: 3.5px; animation-delay: 0ms; }
.ada-run-eq__bar:nth-child(2) { height: 8.2px; animation-delay: 150ms; }
.ada-run-eq__bar:nth-child(3) { height: 5.8px; animation-delay: 300ms; }
.ada-run-eq__bar:nth-child(4) { height: 2.3px; animation-delay: 450ms; }
/* 等待用户答复：HEAD 的「等待你的答复」是静态文案（不滚动、不跳动），四根柱子
   与省略号停住。这里直接摘掉动画而不是 animation-play-state: paused —— 暂停会把
   柱子冻在各自的中间相位（四根高低随机、透明度不一），观感反而像卡住。 */
.ada-run-status[data-run-state="waiting_input"] .ada-run-eq__bar,
.ada-run-status[data-run-state="waiting_input"] .ada-run-status__dots span {
	animation: none;
	opacity: 1;
	transform: none;
}
@media (prefers-reduced-motion: reduce) {
	.ada-run-eq__bar,
	.ada-run-status__dots span { animation: none; opacity: 1; transform: none; }
}
/* 模型选择器（推理等级）：浮层由 setup 里建在 body 上（.ada-model-cascade 的
   定位/边框样式在 app-local.css 里，与 HEAD 同源），模板只剩一个带 aria-haspopup
   的选项按钮。 */
</style>
<script src="./Thread.js"></script>
