import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Wails 会先删掉 frontend/dist 再跑 build，这里不需要额外的 outDir 配置。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 34115,
    strictPort: false,
  },
})
