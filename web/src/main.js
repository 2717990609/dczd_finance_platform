import { createApp } from 'vue'
import {
  Alert, Button, ConfigProvider, Drawer, Empty, Form, Input, InputNumber,
  Select, Table, Tabs, Tag,
} from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import App from './App.vue'
import './style.css'

const app = createApp(App)
for (const component of [Alert, Button, ConfigProvider, Drawer, Empty, Form, Input, InputNumber,
  Select, Table, Tabs, Tag]) app.use(component)
app.mount('#app')
