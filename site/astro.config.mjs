import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://upnexty.com',
  // Compression drops the space at a line break next to an inline tag.
  compressHTML: false,
});
