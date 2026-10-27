import {playHLS} from "./player.js";
const id=location.pathname.split("/").pop();
const player=document.getElementById("video-player");
const status=document.getElementById("playback-status");
let source="", finished=false;
async function loadVideo(){
 try{
  const response=await fetch("/api/videos/"+id,{cache:"no-store"});
  if(!response.ok)throw new Error("Video not found or temporarily unavailable.");
  const {video}=await response.json();
  document.getElementById("video-title").innerHTML=video.title;
  document.getElementById("video-description").innerHTML=video.description;
  document.getElementById("video-date").textContent=new Date(video.created_at).toLocaleString();
  if(!source){player.src=video.video_path;source=video.video_path;}
  if(video.status==="ready"&&video.hls_path){
   status.textContent="Ready — choose Auto for adaptive quality.";
   if(source!==video.hls_path){playHLS(player,video.hls_path,status,document.getElementById("quality"));source=video.hls_path;}
   finished=true;
  }else if(video.status==="failed"){
   status.textContent="Conversion failed. You can still watch the original MP4.";finished=true;
  }else status.textContent="Processing video… The original MP4 is available while quality versions are prepared.";
 }catch(error){status.textContent=error.message;}
 if(!finished)setTimeout(loadVideo,2000);
}
loadVideo();
