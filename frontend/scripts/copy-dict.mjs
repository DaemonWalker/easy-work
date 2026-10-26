import { cp, mkdir } from 'node:fs/promises';

const source = new URL('../node_modules/kuromoji/dict/', import.meta.url);
const destination = new URL('../public/dict/', import.meta.url);

await mkdir(destination, { recursive: true });
await cp(source, destination, { recursive: true });
