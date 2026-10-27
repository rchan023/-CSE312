async function loadVideos() {
 try {
  const response = await fetch("/api/videos",{cache:"no-store"});
  if (!response.ok) throw new Error("Could not load videos.");
  const {videos} = await response.json();
  const list = document.getElementById("video-list");
  list.replaceChildren();
  document.getElementById("videos-status").textContent = videos.length ? "" : "No videos yet.";
  for (const video of videos) {
   const link = document.createElement("a");
   link.href = "/videotube/videos/" + video.id;
   link.className = "block w-72 p-3 bg-gray-800 rounded space-y-2";
   const image = document.createElement("img"); image.src = video.thumbnailURL; image.alt = ""; image.className="w-full h-40 object-cover rounded";
   const title = document.createElement("h2"); title.innerHTML = video.title;
   const status = document.createElement("p");
   status.textContent = ["queued","processing"].includes(video.status) ? "Processing video…" : video.status === "failed" ? "Original video available; conversion failed." : "Ready";
   const date = document.createElement("p"); date.textContent = new Date(video.created_at).toLocaleString();
   link.append(image,title,status,date); list.append(link);
  }
 } catch(error) { document.getElementById("videos-status").textContent=error.message; }
}
loadVideos();
setInterval(loadVideos,5000);
