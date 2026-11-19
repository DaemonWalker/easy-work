import{useCallback,useEffect,useRef,useState}from'react';
import{audioSessionAction,getAudioMeetings,getAudioModels,getAudioSession,saveSettings,startAudioSession,streamEvents}from'../api';
import{audioSourceLabels}from'../config';
import type{AudioMeeting,AudioSegment,AudioSessionState,AudioSource,GlobalTask,ModelInfo,ModelStatus,Settings,Stats,WhisperModel}from'../types';
import StatsView from'./Stats';

type Props={settings:Settings;activeSession?:AudioSessionState;whisperStatus:ModelStatus;task:GlobalTask;busy:boolean;onRefreshState:()=>Promise<void>;onSettings:(settings:Settings)=>void};
type Recognizing={id:string;start_ms:number;end_ms:number};
const modelStatusLabels:Record<string,string>={unloaded:'未加载',loading:'加载中',loaded:'已加载',unloading:'卸载中',failed:'加载失败'};
const sessionStatusLabels:Record<string,string>={recording:'连续会议中',processing:'补全处理中',awaiting_completion:'等待补全',stopped:'已停止',complete:'已完成',saved:'历史会议'};
const validSource=(value:string):AudioSource=>value==='microphone'||value==='both'?value:'system';
const preferredWhisper=(models:WhisperModel[],current:string)=>models.some(m=>m.id===current)?current:models.find(m=>/small/i.test(m.id))?.id||models[0]?.id||'';
const preferredChat=(models:ModelInfo[],current:string)=>models.some(m=>m.id===current)?current:models.find(m=>m.id==='Index-Translate-2B.Q4_K_M.gguf')?.id||models.find(m=>/Index-Translate-2B.*Q4/i.test(m.id))?.id||models[0]?.id||'';
const timeText=(value:string)=>new Date(value).toLocaleString('zh-CN',{hour12:false});
const milliseconds=(value:unknown)=>typeof value==='number'?`${Math.round(value).toLocaleString('zh-CN')} ms`:'—';
const delayText=(value:number)=>value>=1000?`${(value/1000).toFixed(1)} 秒`:`${Math.max(0,value)} ms`;
function positionText(ms:number){const seconds=Math.floor(ms/1000);const hours=Math.floor(seconds/3600);const minutes=Math.floor(seconds%3600/60);const rest=seconds%60;return hours?`${hours}:${String(minutes).padStart(2,'0')}:${String(rest).padStart(2,'0')}`:`${String(minutes).padStart(2,'0')}:${String(rest).padStart(2,'0')}`}
function meetingTitle(meeting:AudioMeeting){return new Date(meeting.created_at).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false})}
function replaceSegment(meeting:AudioMeeting,segment:AudioSegment):AudioMeeting{const index=meeting.segments.findIndex(v=>v.id===segment.id);const segments=[...meeting.segments];if(index<0)segments.push(segment);else segments[index]=segment;return{...meeting,segments}}
function stat(stats:Stats|undefined,key:string){return stats?.[key]}

export default function AudioView({settings,activeSession,whisperStatus,task,busy,onRefreshState,onSettings}:Props){
  const[whisperModels,setWhisperModels]=useState<WhisperModel[]>([]);
  const[chatModels,setChatModels]=useState<ModelInfo[]>([]);
  const[meetings,setMeetings]=useState<AudioMeeting[]>([]);
  const[selected,setSelected]=useState('');
  const[session,setSession]=useState<AudioSessionState|null>(null);
  const[newMeeting,setNewMeeting]=useState(false);
  const[source,setSource]=useState<AudioSource>(validSource(settings.audio_source));
  const[whisperModel,setWhisperModel]=useState(settings.whisper_model);
  const[chatModel,setChatModel]=useState(settings.audio_chat_model);
  const[drafts,setDrafts]=useState<Record<string,string>>({});
  const[recognizing,setRecognizing]=useState<Recognizing|null>(null);
  const[error,setError]=useState('');
  const[notice,setNotice]=useState('');
  const[connection,setConnection]=useState<'idle'|'connecting'|'connected'|'retrying'>('idle');
  const[pendingAction,setPendingAction]=useState('');
  const[collapsed,setCollapsed]=useState(false);
  const[clock,setClock]=useState(Date.now());
  const liveIds=useRef(new Set<string>());
  const selectionVersion=useRef(0);
  const segmentsRef=useRef<HTMLDivElement>(null);
  const followLatest=useRef(true);
  const[showLatest,setShowLatest]=useState(false);
  const returnToLatest=useCallback(()=>{const node=segmentsRef.current;if(!node)return;followLatest.current=true;setShowLatest(false);node.scrollTop=node.scrollHeight},[]);
  const onSegmentsScroll=()=>{const node=segmentsRef.current;if(!node)return;const following=node.scrollHeight-node.scrollTop-node.clientHeight<=48;followLatest.current=following;setShowLatest(!following)};
  const refreshMeetings=useCallback(async()=>{setMeetings(await getAudioMeetings())},[]);
  const selectMeeting=useCallback(async(id:string)=>{const version=++selectionVersion.current;setNewMeeting(false);setSelected(id);setSession(null);setDrafts({});setRecognizing(null);setError('');setNotice('');try{const value=await getAudioSession(id);if(selectionVersion.current!==version)return;setSession(value);setSource(value.meeting.source);setWhisperModel(value.meeting.whisper_model);setChatModel(value.meeting.chat_model)}catch(e){if(selectionVersion.current===version)setError(e instanceof Error?e.message:String(e))}},[]);
  useEffect(()=>{void(async()=>{try{const[modelList,meetingList]=await Promise.all([getAudioModels(),getAudioMeetings()]);setWhisperModels(modelList.whisper);setChatModels(modelList.chat);setMeetings(meetingList);setSource(validSource(settings.audio_source));setWhisperModel(preferredWhisper(modelList.whisper,settings.whisper_model));setChatModel(preferredChat(modelList.chat,settings.audio_chat_model));if(activeSession)await selectMeeting(activeSession.meeting.id);else if(meetingList[0])await selectMeeting(meetingList[0].id);else setNewMeeting(true)}catch(e){setError(e instanceof Error?e.message:String(e))}})()},[]);
  useEffect(()=>{if(activeSession&&selected===activeSession.meeting.id)setSession(activeSession);else if(activeSession&&!selected&&!newMeeting)void selectMeeting(activeSession.meeting.id)},[activeSession,selected,newMeeting,selectMeeting]);
  useEffect(()=>{if(!session?.recording&&!session?.processing)return;const timer=window.setInterval(()=>setClock(Date.now()),1000);return()=>window.clearInterval(timer)},[session?.recording,session?.processing]);
  const isLive=!!selected&&(liveIds.current.has(selected)||activeSession?.meeting.id===selected);
  useEffect(()=>{if(!selected||!isLive){setConnection('idle');return}const controller=new AbortController();let retry=0;setConnection('connecting');const connect=async()=>{try{await streamEvents(`/api/audio/sessions/${encodeURIComponent(selected)}/events`,(event,data)=>{setConnection('connected');if(event==='snapshot'||event==='state'||event==='complete'){setSession(data as AudioSessionState);if(event==='complete'){setRecognizing(null);void refreshMeetings();void onRefreshState()}}else if(event==='recognition_start'){setRecognizing({id:data.segment_id,start_ms:data.start_ms,end_ms:data.end_ms})}else if(event==='recognized'){const segment=data as AudioSegment;setRecognizing(old=>old?.id===segment.id?null:old);setSession(old=>old?{...old,meeting:replaceSegment(old.meeting,segment)}:old);void refreshMeetings()}else if(event==='translation_start'){setDrafts(old=>({...old,[data.segment_id]:''}));setSession(old=>old?{...old,meeting:{...old.meeting,segments:old.meeting.segments.map(segment=>segment.id===data.segment_id?{...segment,status:'translating'}:segment)}}:old)}else if(event==='translation_delta'){setDrafts(old=>({...old,[data.segment_id]:(old[data.segment_id]||'')+data.text}))}else if(event==='translated'){const segment=data as AudioSegment;setSession(old=>old?{...old,meeting:replaceSegment(old.meeting,segment)}:old);setDrafts(old=>{const next={...old};delete next[segment.id];return next});void refreshMeetings()}else if(event==='error'){setError(data.error);setRecognizing(old=>old?.id===data.segment_id?null:old);if(data.segment_id){setDrafts(old=>{const next={...old};delete next[data.segment_id];return next});setSession(old=>old?{...old,meeting:{...old.meeting,segments:old.meeting.segments.map(segment=>segment.id===data.segment_id?{...segment,status:'pending_translation'}:segment)}}:old)}}},controller.signal);if(!controller.signal.aborted){setConnection('retrying');retry=window.setTimeout(()=>void connect(),2000)}}catch(e){if(controller.signal.aborted)return;setConnection('retrying');retry=window.setTimeout(()=>void connect(),2000)}};void connect();return()=>{controller.abort();window.clearTimeout(retry)}},[selected,isLive,refreshMeetings,onRefreshState]);
  useEffect(()=>{followLatest.current=true;setShowLatest(false)},[selected]);
  useEffect(()=>{if(!followLatest.current)return;const node=segmentsRef.current;if(node)node.scrollTop=node.scrollHeight},[session?.meeting.segments,drafts,recognizing]);
  const persist=async(next:Settings)=>{try{onSettings(await saveSettings(next));setError('')}catch(e){setError(e instanceof Error?e.message:String(e))}};
  const begin=async()=>{if(!source||!whisperModel||!chatModel||busy||pendingAction)return;setPendingAction('start');setError('');setNotice('');try{const value=await startAudioSession(source,whisperModel,chatModel);liveIds.current.add(value.meeting.id);++selectionVersion.current;setSelected(value.meeting.id);setSession(value);setNewMeeting(false);setDrafts({});setRecognizing(null);setNotice('连续采集已启动；识别原文会优先出现，中文按顺序后补。');await refreshMeetings();await onRefreshState()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setPendingAction('')}};
  const act=async(action:'recording/stop'|'stop'|'resume')=>{if(!session||pendingAction)return;setPendingAction(action);setError('');try{const value=await audioSessionAction(session.meeting.id,action);if(action==='resume')liveIds.current.add(value.meeting.id);setSession(value);setNotice(action==='recording/stop'?'录音已停止，已采集内容仍会继续识别和翻译。':action==='stop'?'停止请求已发送；已完成的原文和译文会保留。':'正在按顺序补全未完成语音段。');await refreshMeetings();await onRefreshState()}catch(e){setError(e instanceof Error?e.message:String(e))}finally{setPendingAction('')}};
  const createSetup=()=>{++selectionVersion.current;setNewMeeting(true);setSelected('');setSession(null);setDrafts({});setRecognizing(null);setError('');setNotice('');setSource(validSource(settings.audio_source));setWhisperModel(preferredWhisper(whisperModels,settings.whisper_model));setChatModel(preferredChat(chatModels,settings.audio_chat_model))};
  const controlsLocked=busy||!!session?.recording||!!session?.processing||!!pendingAction;
  const canResume=!!session&&isLive&&!session.recording&&!session.processing&&(session.status==='awaiting_completion'||session.status==='stopped');
  const duration=session?Math.max(0,(session.meeting.ended_at?new Date(session.meeting.ended_at).getTime():clock)-new Date(session.meeting.created_at).getTime()):0;
  return<main id="main-content" className={`audio-layout ${collapsed?'sidebar-collapsed':''}`}>
    <aside className="history-sidebar audio-history" aria-label="语音会议历史">
      <div className="sidebar-top"><button className="new-chat-button" data-shortcut="new" onClick={createSetup}>＋ 新建语音会议</button></div>
      <div className="history-label">会议历史 · {meetings.length}</div>
      <div className="history-list">{meetings.length===0&&<p className="empty-history">还没有语音会议</p>}{meetings.map(meeting=>{const completed=meeting.segments.filter(v=>v.status==='complete').length;return<button key={meeting.id} className={`history-item ${selected===meeting.id?'active':''}`} aria-current={selected===meeting.id?'page':undefined} onClick={()=>void selectMeeting(meeting.id)}><span>{meetingTitle(meeting)}</span><small>{audioSourceLabels[meeting.source]} · {completed}/{meeting.segments.length} 段完整</small></button>})}</div>
      <button className="sidebar-toggle" data-shortcut="sidebar" aria-label="收起会议历史" onClick={()=>setCollapsed(true)}><span aria-hidden="true">«</span><span>收起历史</span></button>
    </aside>
    {collapsed&&<button className="sidebar-toggle sidebar-expand" data-shortcut="sidebar" aria-label="展开会议历史" onClick={()=>setCollapsed(false)}><span aria-hidden="true">»</span></button>}
    <section className="audio-workspace" aria-label="连续语音识别翻译">
      <header className="audio-header"><div><h1>连续语音识别翻译</h1><p>本机 WASAPI 持续采集 · 日文原文优先 · 中文按段补全</p></div><span className={`audio-connection ${connection}`} role="status">{connection==='connected'?'实时事件已连接':connection==='retrying'?'实时事件重连中':connection==='connecting'?'正在连接实时事件':'历史快照'}</span></header>
      {task.busy&&<div className="audio-global-busy" role="status">全局模型任务：{task.stage||task.kind}。其他模型功能暂不可用，不会排队。</div>}
      <section className="audio-controls" aria-label="会议设置与操作">
        <div className="audio-selectors">
          <label>音源<select data-shortcut="input" aria-label="音源" value={source} disabled={controlsLocked} onChange={e=>{const value=e.target.value as AudioSource;setSource(value);void persist({...settings,audio_source:value})}}>{Object.entries(audioSourceLabels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>
          <label>Whisper 模型<select aria-label="Whisper 模型" value={whisperModel} disabled={controlsLocked||whisperModels.length===0} onChange={e=>{setWhisperModel(e.target.value);void persist({...settings,whisper_model:e.target.value})}}><option value="">请选择 Whisper 模型</option>{whisperModels.map(model=><option key={model.id} value={model.id}>{model.name}</option>)}</select></label>
          <label>翻译模型<select aria-label="语音翻译模型" value={chatModel} disabled={controlsLocked||chatModels.length===0} onChange={e=>{setChatModel(e.target.value);void persist({...settings,audio_chat_model:e.target.value})}}><option value="">请选择日译中模型</option>{chatModels.map(model=><option key={model.id} value={model.id}>{model.name}</option>)}</select></label>
        </div>
        <div className="audio-actions">
          {newMeeting&&<button className="primary-button" data-shortcut="record" disabled={!source||!whisperModel||!chatModel||busy||!!pendingAction} onClick={()=>void begin()}>{pendingAction==='start'?'正在启动…':'开始连续录音'}</button>}
          {session&&isLive&&session.recording&&<button className="secondary-button" data-shortcut="record" disabled={!!pendingAction} onClick={()=>void act('recording/stop')}>{pendingAction==='recording/stop'?'正在停止采集…':'停止录音，继续处理'}</button>}
          {canResume&&<button className="primary-button" data-shortcut="resume" disabled={busy||!!pendingAction} onClick={()=>void act('resume')}>{pendingAction==='resume'?'正在启动补全…':'会后补全'}</button>}
        </div>
      </section>
      {(!whisperModels.length||!chatModels.length)&&<div className="conversation-error" role="alert">未找到可用模型。请准备 gguf/whisper/ 中的 Whisper 模型和 gguf/chat/ 中的日译中模型。</div>}
      <section className="audio-status-panel" aria-label="采集与模型状态">
        <div><span>录音状态</span><strong>{session?.recording?'录音中':'已停止采集'}</strong></div>
        <div><span>处理状态</span><strong>{session?sessionStatusLabels[session.status]||session.status:'未开始'}</strong></div>
        <div><span>Whisper 实际模型</span><strong>{modelStatusLabels[whisperStatus.status]} · {whisperStatus.model||'无'}</strong></div>
        <div><span>采集时长</span><strong>{session?positionText(duration):'00:00'}</strong></div>
        <div><span>待识别 / 实际延迟</span><strong>{session?.pending_recognition||0} 段 / {delayText(session?.recognition_delay_ms||0)}</strong></div>
        <div><span>待翻译 / 实际延迟</span><strong>{session?.pending_translation||0} 段 / {delayText(session?.translation_delay_ms||0)}</strong></div>
      </section>
      {(error||session?.error)&&<div className="conversation-error" role="alert"><strong>会议处理提示</strong><span>{error||session?.error}</span></div>}
      {notice&&<p className="audio-notice" role="status">{notice}</p>}
      {session?<section className="audio-transcript" aria-label="连续原文与中文字幕">
        <div className="audio-transcript-heading"><h2>会议字幕 <small>{session.meeting.segments.length} 段</small></h2><span>日文原文</span><span>中文译文</span></div>
        <div className="audio-segments" ref={segmentsRef} onScroll={onSegmentsScroll} tabIndex={0} aria-label="会议字幕列表">{session.meeting.segments.length===0&&!recognizing&&<div className="audio-empty"><strong>{session.recording?'正在等待语音…':'还没有完整识别原文'}</strong><p>录音持续进行，语音段完成后先显示原文。</p></div>}
          {session.meeting.segments.map(segment=>{const draft=drafts[segment.id];const complete=segment.status==='complete';const translating=!complete&&(segment.status==='translating'||draft!==undefined);return<article className="audio-segment" key={segment.id}>
            <header><span className="audio-timecode">{positionText(segment.start_ms)} → {positionText(segment.end_ms)}</span><span className={`audio-segment-status ${complete?'complete':translating?'translating':'pending'}`}>{complete?'完整':translating?'翻译中':'待翻译'}</span></header>
            <div className="audio-source-text"><p>{segment.source}</p></div>
            <div className={`audio-translation-text ${complete?'complete':'draft'}`}>{translating&&<span>中文草稿 · 尚未完成</span>}{complete?<p>{segment.translation}</p>:translating?<p>{draft||'等待首个译文片段…'}<i className="cursor" aria-hidden="true"/></p>:<p className="muted">待翻译</p>}
              <details className="audio-segment-details"><summary>技术详情</summary><div><small>识别完成：{timeText(segment.recognized_at)} · 识别耗时 {milliseconds(stat(segment.recognition_stats,'elapsed_ms'))}</small>{complete&&<small>翻译完成：{segment.translated_at?timeText(segment.translated_at):'—'} · 首 Token {milliseconds(stat(segment.translation_stats,'ttft_ms'))} · 翻译耗时 {milliseconds(stat(segment.translation_stats,'elapsed_ms'))}</small>}{segment.translation_stats&&<StatsView stats={segment.translation_stats}/>}</div></details>
            </div>
          </article>})}
          {recognizing&&<article className="audio-segment recognizing"><header><span className="audio-timecode">{positionText(recognizing.start_ms)} → {positionText(recognizing.end_ms)}</span><span className="audio-segment-status translating">识别中</span></header><p><i className="spinner"/> 正在识别该语音段，完整原文尚未生成。</p></article>}
        </div>
        {showLatest&&<button className="audio-latest" onClick={returnToLatest}>↓ 回到最新</button>}
      </section>:<section className="audio-empty audio-setup"><h2>{newMeeting?'准备新的连续会议':'正在读取会议…'}</h2><p>选择音源和模型后开始。电脑内声音是系统混音；混合模式同时采集系统声音和麦克风。</p><p>停止录音不会中止补全；停止整个会话才会取消当前识别或翻译。</p></section>}
      {session&&isLive&&(session.recording||session.processing)&&<button className="audio-floating-stop" data-shortcut="stop" aria-label="停止整个会话" title="停止整个会话（Alt+X / Ctrl+.）" disabled={!!pendingAction} onClick={()=>void act('stop')}><span aria-hidden="true"/></button>}
    </section>
  </main>
}
