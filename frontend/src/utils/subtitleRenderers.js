import {subtitleVTT,normalizeSubtitlePreferences} from './playbackSubtitles.js';
const defaults={enabled:true,offset_ms:0,preferences:normalizeSubtitlePreferences(null)};
export class WebSubtitleRenderer {
 constructor(media,onError){this.media=media;this.onError=onError;this.configuration=defaults;this.cues=[];this.position=this.position.bind(this);this.surface=media.parentElement;this.surface?.addEventListener('playback-controls-resize',this.position);if(typeof ResizeObserver!=='undefined'){this.resize=new ResizeObserver(this.position);this.resize.observe(media);}if(this.surface){this.visibility=new MutationObserver(this.position);this.visibility.observe(this.surface,{attributes:true,attributeFilter:['userinactive','mediapaused']});}}
 setDocument(document){this.cues=document.cues||[];this.dirty=true;}
 configure(configuration){const old=this.configuration;this.configuration={...defaults,...configuration,preferences:normalizeSubtitlePreferences(configuration.preferences)};if(this.dirty||old.offset_ms!==this.configuration.offset_ms){this.removeTrack();this.blob=URL.createObjectURL(new Blob([subtitleVTT(this.cues,this.configuration.offset_ms/1000)],{type:'text/vtt'}));const track=document.createElement('track');track.kind='subtitles';track.label='外置字幕';track.srclang='und';track.src=this.blob;track.default=true;track.addEventListener('load',()=>{if(this.track===track)this.mode();});track.addEventListener('error',()=>{if(this.track===track)this.onError('播放器无法加载字幕，请刷新字幕');});this.track=track;this.media.appendChild(track);this.dirty=false;}this.mode();}
 mode(){if(this.track?.track)this.track.track.mode=this.configuration.enabled?'showing':'disabled';this.position();}
 position(){const video=this.media,surface=this.surface,height=video.getBoundingClientRect().height;if(!height||!surface)return;const width=video.getBoundingClientRect().width||0,tokens=getComputedStyle(video),preferences=this.configuration.preferences;
 const limits={small:['--fs-caption','--fs-title',.025],medium:['--fs-body','--fs-display',.03],large:['--fs-section','--fs-hero',.04],xlarge:['--fs-title','--fs-hero',.05]};const [minToken,maxToken,ratio]=limits[preferences.size];const minimum=parseFloat(tokens.getPropertyValue(minToken))||14,maximum=parseFloat(tokens.getPropertyValue(maxToken))||40;
 video.style.setProperty('--fs-subtitle-active',`${Math.max(minimum,Math.min(maximum,width*ratio))}px`);video.style.setProperty('--subtitle-background',{none:'transparent',translucent:'var(--media-badge-bg)',solid:'var(--player-bg)'}[preferences.background]);
 const hidden=surface.hasAttribute('userinactive')&&!video.paused,barHeight=hidden?0:(surface.querySelector('[data-test="playback-controls"]')?.getBoundingClientRect().height||0);
 for(const cue of this.track?.track?.cues||[]){cue.snapToLines=false;cue.lineAlign={bottom:'end',middle:'center',top:'start'}[preferences.position];cue.line=preferences.position==='bottom'?Math.max(10,Math.min(95,(height-barHeight-12)/height*100)):preferences.position==='middle'?50:5;}}
 removeTrack(){this.track?.remove();this.track=null;if(this.blob)URL.revokeObjectURL(this.blob);this.blob=null;}
 clear(){this.removeTrack();this.cues=[];}
 dispose(){this.clear();this.resize?.disconnect();this.visibility?.disconnect();this.surface?.removeEventListener('playback-controls-resize',this.position);this.media.style.removeProperty('--fs-subtitle-active');this.media.style.removeProperty('--subtitle-background');}
}
export class NativeSubtitleRenderer {
 constructor(media,onError){this.media=media;this.onError=onError;this.generation=0;this.configuration=defaults;}
 setDocument(document){this.fingerprint=document.fingerprint||'';}
 async configure(configuration){this.configuration={...defaults,...configuration,preferences:normalizeSubtitlePreferences(configuration.preferences)};const generation=++this.generation;try{await this.media.configureSubtitles({...this.configuration,subtitle_fingerprint:this.fingerprint});}catch(error){if(generation===this.generation&&!this.disposed)this.onError(String(error?.message||error));}}
 clear(){++this.generation;return this.media.configureSubtitles({...this.configuration,enabled:false,subtitle_fingerprint:''}).catch(()=>{});}
 dispose(){this.disposed=true;this.clear();}
}
