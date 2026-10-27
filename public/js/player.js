export function playHLS(player, path, status, select, live = false) {
 if (window.Hls && Hls.isSupported()) {
  // Live streams choose a position near the live edge.
 const position = live ? -1 : (player.currentTime || 0);
  const wasPlaying = !player.paused;
  const hls = new Hls({startPosition:position});
  hls.loadSource(path); hls.attachMedia(player);
  hls.on(Hls.Events.MANIFEST_PARSED, (_,data) => {
   if (select) {
    select.replaceChildren(new Option("Auto","-1"));
    data.levels.forEach((level,index) => select.add(new Option(level.height+"p",String(index))));
    document.getElementById("quality-label").hidden=false;
    select.onchange=()=>{hls.currentLevel=Number(select.value);};
   }
   if (wasPlaying) player.play().catch(()=>{});
  });
  hls.on(Hls.Events.ERROR, (_,data) => {
   if (data.fatal) {
    const code = data.response?.code;
    status.textContent = "Playback error: " + data.type + " / " + data.details + (code ? " (HTTP " + code + ")" : "") + ". Refresh to reconnect.";
   }
  });
  window.addEventListener("pagehide",()=>hls.destroy(),{once:true});
  return hls;
 }
 if (player.canPlayType("application/vnd.apple.mpegurl")) {player.src=path;return null;}
 status.textContent="HLS playback is unavailable in this browser. The original video remains available.";
 return null;
}
