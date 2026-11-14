<template>
  <div v-if="items.length > 0" class="aq-list">
    <p v-if="failed" class="aq-card__prompt" role="alert">{{ failed }}</p>
    <div v-for="item in items" :key="item.id" class="aq-card">
      <div class="aq-card__head">
        <span class="aq-card__title">{{ item.title || "待答复" }}</span>
        <span class="aq-card__meta">等待回答</span>
      </div>
      <p v-if="item.body" class="aq-card__prompt">{{ item.body }}</p>
      <div v-if="item.kind === 'select' && item.options.length > 0" class="aq-card__options">
        <label v-for="(opt, oi) in item.options" :key="oi" class="aq-card__option">
          <input type="radio" :name="'aq-value-' + item.id" :checked="picked[item.id] === opt" @change="picked[item.id] = opt" />
          <span class="aq-card__option-label">{{ opt }}</span>
        </label>
      </div>
      <input v-if="item.kind === 'input'" class="ada-input aq-card__text" type="text" v-model="answers[item.id]" placeholder="输入你的回答" />
      <div class="aq-card__actions">
        <span v-if="item.kind === 'confirm'" class="aq-card__buttons">
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="submit(item, 'deny')">拒绝</button>
          <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="submit(item, 'accept')">接受</button>
        </span><span v-else class="aq-card__buttons">
          <button class="ada-btn ada-btn--primary ada-btn--sm" type="button" @click="submit(item, 'accept')">提交</button>
          <button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" @click="submit(item, 'deny')">拒绝</button>
        </span>
        <button class="ada-btn ada-btn--ghost ada-btn--sm" type="button" @click="submit(item, 'cancel')">取消</button>
      </div>
    </div>
  </div>
</template>
<script src="./AskTray.js"></script>
