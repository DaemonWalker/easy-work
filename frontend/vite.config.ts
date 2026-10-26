import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({ base: './', plugins: [react()], resolve:{alias:[{find:/^path$/,replacement:'/src/browser-path.ts'}]}, build: { outDir: 'dist' } });
