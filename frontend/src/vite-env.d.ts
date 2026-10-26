/// <reference types="vite/client" />
declare module 'kuroshiro' {
  export default class Kuroshiro {
    init(analyzer: unknown): Promise<void>;
    convert(text: string, options: {to: string; mode: string; romajiSystem: string}): Promise<string>;
  }
}
declare module 'kuroshiro-analyzer-kuromoji' {
  export default class KuromojiAnalyzer { constructor(options: {dictPath: string}); }
}
