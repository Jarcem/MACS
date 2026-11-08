import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// macs-tower-web 构建配置（P1-7）；Tailwind/shadcn 初始化随 M5 UI 里程碑进行
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 5173,
    proxy: {
      // 开发期直连调度塔管理端口（Q9：8471）
      '/api': { target: 'http://127.0.0.1:8471', changeOrigin: true },
    },
  },
});
