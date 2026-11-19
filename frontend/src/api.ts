import type{AudioMeeting,AudioSessionState,AudioSource,Conversation,ImageTask,ModelInfo,Settings,State,WhisperModel}from'./types';
export async function request<T>(url:string,init?:RequestInit):Promise<T>{const response=await fetch(url,init);if(!response.ok){let message=`HTTP ${response.status}`;try{const body=await response.json() as{error?:string};message=body.error||message}catch{/* ignore */}throw new Error(message)}return response.json() as Promise<T>}
export const getState=()=>request<State>('/api/state');
export const getConversations=(tab:string)=>request<Conversation[]>(`/api/conversations?tab=${tab}`);
export const getImages=()=>request<ImageTask[]>('/api/images');
export const saveSettings=(settings:Settings)=>request<Settings>('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(settings)});
export function savePreferences(body:Record<string,unknown>){return request<Settings>('/api/preferences',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})}
export type SSEHandler=(event:string,data:any)=>void;
async function readSSE(response:Response,onEvent:SSEHandler){if(!response.body)throw new Error('浏览器不支持流式响应');const reader=response.body.getReader();const decoder=new TextDecoder();let buffer='';const consume=(block:string)=>{const lines=block.split(/\r?\n/);let event='message',data='';for(const line of lines){if(line.startsWith('event:'))event=line.slice(6).trim();else if(line.startsWith('data:'))data+=(data?'\n':'')+line.slice(5).trimStart()}if(data)onEvent(event,JSON.parse(data))};while(true){const{done,value}=await reader.read();buffer+=decoder.decode(value||new Uint8Array(),{stream:!done});const parts=buffer.split(/\r?\n\r?\n/);buffer=parts.pop()||'';parts.forEach(consume);if(done)break}if(buffer.trim())consume(buffer)}
async function sseResponse(url:string,init?:RequestInit){const response=await fetch(url,init);if(!response.ok){let message=`HTTP ${response.status}`;try{const payload=await response.json() as{error?:string};message=payload.error||message}catch{/* ignore */}throw new Error(message)}return response}
export async function stream(url:string,body:BodyInit|undefined,onEvent:SSEHandler,headers:Record<string,string>={}){const response=await sseResponse(url,{method:'POST',body,headers});await readSSE(response,onEvent)}
export async function streamEvents(url:string,onEvent:SSEHandler,signal:AbortSignal){const response=await sseResponse(url,{headers:{Accept:'text/event-stream'},signal});await readSSE(response,onEvent)}
export const jsonHeaders={'Content-Type':'application/json'};
export const getAudioModels=()=>request<{whisper:WhisperModel[];chat:ModelInfo[];sources:AudioSource[]}>('/api/audio/models');
export const getAudioMeetings=()=>request<AudioMeeting[]>('/api/audio/meetings');
export const getAudioSession=(id:string)=>request<AudioSessionState>(`/api/audio/sessions/${encodeURIComponent(id)}`);
export const startAudioSession=(source:AudioSource,whisperModel:string,chatModel:string)=>request<AudioSessionState>('/api/audio/sessions',{method:'POST',headers:jsonHeaders,body:JSON.stringify({source,whisper_model:whisperModel,chat_model:chatModel})});
export const audioSessionAction=(id:string,action:'recording/stop'|'stop'|'resume')=>request<AudioSessionState>(`/api/audio/sessions/${encodeURIComponent(id)}/${action}`,{method:'POST'});
