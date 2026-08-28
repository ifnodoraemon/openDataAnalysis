import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import "./assets/styles/global.css";

const app = createApp(App);
app.use(createPinia());

app.config.errorHandler = (err, instance, info) => {
  console.error("[界面错误]", info, err);
};

app.mount("#app");
