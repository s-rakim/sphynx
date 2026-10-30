// Bundles fallback.js and the noble libraries it uses into one file the
// chat loads on plain http pages, with the libraries' licenses on top.
import fs from 'node:fs';
import { build } from 'esbuild';

const libs = ['curves', 'ciphers', 'hashes'].map((name) => {
  const pkg = JSON.parse(fs.readFileSync(`node_modules/@noble/${name}/package.json`, 'utf8'));
  return { name: pkg.name, version: pkg.version, license: fs.readFileSync(`node_modules/@noble/${name}/LICENSE`, 'utf8').trim() };
});
const licenses = [...new Set(libs.map((l) => l.license))].join('\n\n');
const banner = `/*! Sphynx Chat WebCrypto fallback: built from internal/chat/webcrypto/fallback.js
 * with ${libs.map((l) => `${l.name} ${l.version}`).join(', ')}. Do not edit; run npm run build there.
 *
${licenses.split('\n').map((line) => ` * ${line}`.trimEnd()).join('\n')}
 */`;

await build({
  entryPoints: ['fallback.js'],
  bundle: true,
  format: 'esm',
  minify: true,
  target: 'es2020',
  legalComments: 'none',
  banner: { js: banner },
  outfile: '../web/vendor/webcrypto-fallback.js',
});
