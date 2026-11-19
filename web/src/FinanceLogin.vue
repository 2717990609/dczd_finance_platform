<script setup>
import { ref } from 'vue'
import { BankOutlined } from '@ant-design/icons-vue'

const props = defineProps({ api:Function, development:Boolean })
const emit = defineEmits(['signedIn','cancel'])
const login = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function signIn() {
  if (busy.value || !login.value.trim() || !password.value) return
  busy.value = true
  error.value = ''
  try {
    await props.api('/session',{method:'POST',body:JSON.stringify({login:login.value,password:password.value})})
    password.value = ''
    emit('signedIn')
  } catch (err) {
    error.value = err.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="account-screen">
    <section class="account-card">
      <span class="brand-icon"><BankOutlined aria-hidden="true"/></span>
      <h1>财务工作台</h1>
      <a-form layout="vertical" @submit.prevent="signIn">
        <a-form-item label="账号" html-for="finance-login">
          <a-input id="finance-login" v-model:value="login" autocomplete="username" :maxlength="80" required/>
        </a-form-item>
        <a-form-item label="密码" html-for="finance-password">
          <a-input-password id="finance-password" v-model:value="password" autocomplete="current-password" required/>
        </a-form-item>
        <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
        <a-button html-type="submit" type="primary" size="large" :loading="busy" block>登录</a-button>
        <a-button v-if="development" type="link" block :disabled="busy" @click="emit('cancel')">返回工作台</a-button>
      </a-form>
    </section>
  </main>
</template>

<style scoped>
.account-screen { min-height: 100vh; display: grid; place-items: center; padding: 24px 16px; }
.account-card { width: min(100%, 390px); padding: 30px; background: var(--panel); border: 1px solid var(--line); border-radius: 8px; }
.account-card h1 { margin: 15px 0 24px; font-size: 24px; font-weight: 680; }
</style>
