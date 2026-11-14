<template>
  <details v-if="visible" class="todo-card" :class="complete ? 'is-done' : ''">
    <summary class="todo-card__summary" title="点击展开 Todo 列表">
      <span class="todo-card__head">
        <span class="ada-icon" data-icon="i-list" aria-hidden="true">≡</span>
        <span class="todo-card__title">Todo 列表</span>
        <span class="todo-card__count">{{ done }}/{{ total }}</span>
      </span>
    </summary>
    <div class="todo-card__body">
      <div class="ada-todo__list-wrap">
        <ol class="ada-todo__list">
          <li v-for="(step, i) in steps" :key="i" :class="stepClass(step)">
            <span class="ada-icon" :data-icon="stepIcon(step)" aria-hidden="true">{{ stepMark(step) }}</span>
            <span>{{ step.step }}</span>
          </li>
        </ol>
      </div>
    </div>
  </details>
</template>
<script src="./PlanCard.js"></script>
