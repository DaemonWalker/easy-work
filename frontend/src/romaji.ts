import Kuroshiro from'kuroshiro';import KuromojiAnalyzer from'kuroshiro-analyzer-kuromoji';import type{RomajiMode}from'./types';
let instance:Kuroshiro|undefined;let initialization:Promise<Kuroshiro>|undefined;
export function getKuroshiro(){if(instance)return Promise.resolve(instance);if(!initialization){const kuroshiro=new Kuroshiro();initialization=kuroshiro.init(new KuromojiAnalyzer({dictPath:'./dict'})).then(()=>{instance=kuroshiro;return kuroshiro}).catch(error=>{initialization=undefined;throw error})}return initialization}
export async function toRomaji(text:string,mode:RomajiMode){if(!text.trim())return'';const kuroshiro=await getKuroshiro();return kuroshiro.convert(text,{to:'romaji',mode,romajiSystem:'hepburn'})}
