import type{ModelTab,RomajiMode,Tab}from'./types';
export const tabs:{id:Tab;label:string}[]=[{id:'colleague',label:'同事交流'},{id:'document',label:'设计书'},{id:'testcase',label:'Test Case'},{id:'ja2zh',label:'日译中'},{id:'images',label:'图像识别翻译'},{id:'custom',label:'自由 Chat'},{id:'romaji',label:'罗马音'}];
export const modelTabs:ModelTab[]=['colleague','document','testcase','ja2zh','custom'];
export const tabLabels:Record<ModelTab,string>={colleague:'同事交流',document:'设计书／式样书',testcase:'Test Case',ja2zh:'日译中',custom:'自由 Chat'};
export const placeholders:Record<ModelTab,string>={colleague:'输入中文短句；后续也可以输入修正要求',document:'输入设计书／式样书中的中文短句',testcase:'输入一句操作步骤、确认项或期待值',ja2zh:'输入需要理解的日文',custom:'输入完整 Prompt，自行描述任务和内容'};
export const translationModels=['Hy-MT2-7B-Q4_K_M.gguf','HY-MT2-7B-Q6_K.gguf'];
export const romajiModes:{value:RomajiMode;label:string}[]=[{value:'spaced',label:'分词（spaced）'},{value:'normal',label:'连续（normal）'},{value:'okurigana',label:'送假名（okurigana）'},{value:'furigana',label:'注音（furigana）'}];
export const isC2J=(tab:ModelTab)=>tab==='colleague'||tab==='document'||tab==='testcase';
export const themes=[{group:'浅色',items:[['cloud','云雾白'],['paper','暖纸米'],['green','护眼绿'],['ice','冰川蓝']]},{group:'深色',items:[['graphite','石墨灰'],['midnight','午夜蓝'],['black','纯黑']]},{group:'个性',items:[['sakura','樱花粉'],['lavender','薰衣草紫'],['sunset','落日橙']]},{group:'无障碍',items:[['contrast-light','高对比浅色'],['contrast-dark','高对比深色']]}];
