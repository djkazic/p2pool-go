package web

// dashboardHTML is the embedded HTML/CSS/JS for the web dashboard.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>p2pool Dashboard</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0d1117;color:#c9d1d9;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;padding:24px;min-height:100vh}
h1{font-size:1.5rem;font-weight:600;color:#f0f6fc;margin-bottom:4px}
.subtitle{color:#8b949e;font-size:0.85rem;margin-bottom:24px}
.subtitle span{color:#58a6ff}
.stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:16px;margin-bottom:24px}
.card{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:20px}
.card .label{color:#8b949e;font-size:0.75rem;text-transform:uppercase;letter-spacing:0.5px;margin-bottom:8px}
.card .value{font-size:1.75rem;font-weight:700;color:#f0f6fc;font-family:"SF Mono",SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace}
.card .value.accent{color:#58a6ff}
.section{margin-bottom:24px}
.grid2{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:24px}
@media(max-width:900px){.grid2{grid-template-columns:1fr}}
.card h2{font-size:0.9rem;font-weight:600;color:#f0f6fc;margin-bottom:12px}
table{width:100%;border-collapse:collapse}
th{text-align:left;color:#8b949e;font-size:0.7rem;text-transform:uppercase;letter-spacing:0.5px;padding:6px 8px;border-bottom:1px solid #30363d}
td{padding:8px;font-size:0.8rem;border-bottom:1px solid #21262d;font-family:"SF Mono",SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace}
td.miner{max-width:200px}
td.hash{color:#8b949e;cursor:pointer;word-break:break-all;white-space:normal}
td.hash:hover{color:#58a6ff;text-decoration:underline}
a.addr{color:#58a6ff;text-decoration:none;word-break:break-all}
a.addr:hover{text-decoration:underline}
td.time{color:#c9d1d9;white-space:nowrap}
/* Golden share (Bitcoin block found) */
tr.golden td{background:rgba(187,128,9,0.08)}
tr.golden td.hash{color:#e3b341}
tr.golden td.hash:hover{color:#f0c040}
.golden-badge{display:inline-block;background:#e3b341;color:#0d1117;font-size:0.6rem;font-weight:700;padding:1px 5px;border-radius:3px;margin-left:6px;vertical-align:middle;letter-spacing:0.3px}
.bar-chart{display:flex;flex-direction:column;gap:8px}
.bar-row{display:flex;align-items:center;gap:10px}
.bar-label{font-size:0.75rem;font-family:"SF Mono",SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace;min-width:120px;max-width:260px;word-break:break-all}
.bar-label a.addr{font-size:inherit}
.bar-track{flex:1;background:#21262d;border-radius:4px;height:20px;overflow:hidden}
.bar-fill{height:100%;background:linear-gradient(90deg,#1f6feb,#58a6ff);border-radius:4px;transition:width 0.4s ease}
.bar-pct{font-size:0.75rem;color:#8b949e;min-width:50px;text-align:right;font-family:"SF Mono",SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace}
.info{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:8px;margin-top:12px}
.info-item{font-size:0.75rem;color:#8b949e}
.info-item span{color:#c9d1d9}
.no-data{color:#484f58;font-size:0.85rem;font-style:italic;padding:16px 0}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:#3fb950;margin-right:6px;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:0.4}}

/* Graphs */
.graph-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-bottom:24px}
@media(max-width:900px){.graph-grid{grid-template-columns:1fr}}
.graph-card{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:20px}
.graph-card h2{font-size:0.9rem;font-weight:600;color:#f0f6fc;margin-bottom:12px}
.graph-wrap{position:relative;width:100%;height:160px}
canvas{width:100%;height:100%}

/* Share detail modal */
.modal-overlay{display:none;position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,0.6);z-index:100;justify-content:center;align-items:center}
.modal-overlay.open{display:flex}
.modal{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:24px;max-width:600px;width:90%;max-height:80vh;overflow-y:auto}
.modal h2{font-size:1.1rem;font-weight:600;color:#f0f6fc;margin-bottom:16px}
.modal .close-btn{float:right;background:none;border:none;color:#8b949e;font-size:1.2rem;cursor:pointer;padding:4px 8px}
.modal .close-btn:hover{color:#f0f6fc}
.modal dl{display:grid;grid-template-columns:auto 1fr;gap:6px 12px}
.modal dt{color:#8b949e;font-size:0.75rem;text-transform:uppercase;letter-spacing:0.5px;padding-top:4px}
.modal dd{font-family:"SF Mono",SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace;font-size:0.8rem;word-break:break-all;padding:4px 0}
.modal dd.golden{color:#e3b341;font-weight:600}
.modal .loading{color:#484f58;font-style:italic;padding:20px 0;text-align:center}
</style>
</head>
<body>
<h1>p2pool Dashboard</h1>
<p class="subtitle"><span class="dot"></span>Auto-refreshing &middot; <span id="network">-</span> &middot; Uptime: <span id="uptime">-</span></p>

<div class="stats">
  <div class="card"><div class="label">Pool Hashrate</div><div class="value accent" id="pool-hashrate">-</div></div>
  <div class="card"><div class="label">Local Hashrate</div><div class="value" id="local-hashrate">-</div></div>
  <div class="card"><div class="label">Miners | Peers</div><div class="value" id="miners-peers">-</div></div>
  <div class="card"><div class="label">Shares</div><div class="value" id="shares">-</div></div>
  <div class="card"><div class="label">Difficulty</div><div class="value" id="difficulty">-</div></div>
  <div class="card"><div class="label">Est. Time to Block</div><div class="value" id="ttb">-</div></div>
  <div class="card"><div class="label">Last Block Found</div><div class="value" id="last-block">-</div></div>
</div>

<div class="graph-grid">
  <div class="graph-card">
    <h2>Pool Hashrate</h2>
    <div class="graph-wrap"><canvas id="graph-hashrate"></canvas></div>
  </div>
  <div class="graph-card">
    <h2>Share Count</h2>
    <div class="graph-wrap"><canvas id="graph-shares"></canvas></div>
  </div>
</div>

<div class="grid2">
  <div class="card">
    <h2>Recent Shares</h2>
    <table>
      <thead><tr><th>Hash</th><th>Miner</th><th>Time</th></tr></thead>
      <tbody id="shares-table"><tr><td colspan="3" class="no-data">No shares yet</td></tr></tbody>
    </table>
  </div>
  <div class="card">
    <h2>PPLNS Distribution</h2>
    <div class="bar-chart" id="pplns-chart"><div class="no-data">No data yet</div></div>
  </div>
</div>

<div class="card">
  <h2>Node Info</h2>
  <div class="info">
    <div class="info-item">Tip: <span id="tip-hash">-</span></div>
    <div class="info-item">Tip Miner: <span id="tip-miner">-</span></div>
    <div class="info-item">Target: <span id="target-bits">-</span></div>
    <div class="info-item">Stratum Port: <span id="stratum-port">-</span></div>
    <div class="info-item">P2P Port: <span id="p2p-port">-</span></div>
    <div class="info-item">Share Target Time: <span id="share-target-time">-</span></div>
    <div class="info-item">PPLNS Window: <span id="pplns-window">-</span></div>
  </div>
</div>

<!-- Share detail modal -->
<div class="modal-overlay" id="share-modal">
  <div class="modal">
    <button class="close-btn" onclick="closeModal()">&times;</button>
    <h2>Share Details</h2>
    <div id="share-detail-content"><div class="loading">Loading...</div></div>
  </div>
</div>

<script>
/* ---- Utility functions ---- */
function timeAgo(ts){
  if(!ts)return"-";
  var d=Math.floor(Date.now()/1000)-ts;
  if(d<0)return"just now";
  if(d<60)return d+"s ago";
  if(d<3600)return Math.floor(d/60)+"m ago";
  if(d<86400)return Math.floor(d/3600)+"h ago";
  return Math.floor(d/86400)+"d ago";
}
function fmtUptime(s){
  if(!s)return"-";
  var d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60);
  if(d>0)return d+"d "+h+"h "+m+"m";
  if(h>0)return h+"h "+m+"m";
  return m+"m "+Math.floor(s%60)+"s";
}
function truncate(s,n){
  if(!s)return"-";
  if(s.length<=n)return s;
  return s.substring(0,n)+"...";
}
function esc(s){
  if(!s)return"";
  return s.replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;").replace(/'/g,"&#39;");
}
var _net="";
function addrLink(addr){
  if(!addr)return"-";
  var prefix=(_net==="mainnet"||!_net)?"":"/"+ _net.replace("testnet3","testnet");
  var url="https://mempool.space"+prefix+"/address/"+encodeURIComponent(addr);
  return'<a class="addr" href="'+esc(url)+'" target="_blank" rel="noopener">'+esc(addr)+'</a>';
}
function fmtDiff(d){
  if(d>=1e12)return(d/1e12).toFixed(2)+"T";
  if(d>=1e9)return(d/1e9).toFixed(2)+"G";
  if(d>=1e6)return(d/1e6).toFixed(2)+"M";
  if(d>=1e3)return(d/1e3).toFixed(2)+"K";
  return d.toFixed(2);
}
function fmtHash(h){
  if(!h||h===0)return"0 H/s";
  if(h>=1e18)return(h/1e18).toFixed(2)+" EH/s";
  if(h>=1e15)return(h/1e15).toFixed(2)+" PH/s";
  if(h>=1e12)return(h/1e12).toFixed(2)+" TH/s";
  if(h>=1e9)return(h/1e9).toFixed(2)+" GH/s";
  if(h>=1e6)return(h/1e6).toFixed(2)+" MH/s";
  if(h>=1e3)return(h/1e3).toFixed(2)+" KH/s";
  return h.toFixed(2)+" H/s";
}
function fmtDuration(s){
  if(!s||s<=0)return"-";
  var d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60);
  if(d>36500)return"\u221e";
  if(d>365)return(d/365).toFixed(1)+"y";
  if(d>0)return d+"d "+h+"h";
  if(h>0)return h+"h "+m+"m";
  return m+"m";
}
function fmtTimestamp(ts){
  if(!ts)return"-";
  return new Date(ts*1000).toLocaleString();
}

/* ---- Graph history (ring buffers) ---- */
var MAX_POINTS=60;
var histHashrate=[];
var histShares=[];
var histSeeded=false;
function pushHistory(arr,val){arr.push(val);if(arr.length>MAX_POINTS)arr.shift();}

/* ---- Canvas graph renderer ---- */
function drawGraph(canvasId,data,color,formatter){
  var canvas=document.getElementById(canvasId);
  if(!canvas)return;
  var ctx=canvas.getContext("2d");
  var dpr=window.devicePixelRatio||1;
  var rect=canvas.parentElement.getBoundingClientRect();
  canvas.width=rect.width*dpr;
  canvas.height=rect.height*dpr;
  ctx.scale(dpr,dpr);
  var W=rect.width,H=rect.height;
  ctx.clearRect(0,0,W,H);
  if(data.length<2)return;

  var max=0;for(var i=0;i<data.length;i++){if(data[i]>max)max=data[i];}
  if(max===0)max=1;
  var pad=24;
  var gW=W-pad,gH=H-pad;

  // Grid lines
  ctx.strokeStyle="#21262d";
  ctx.lineWidth=1;
  for(var g=0;g<=4;g++){
    var gy=pad/2+gH*(1-g/4);
    ctx.beginPath();ctx.moveTo(pad,gy);ctx.lineTo(W,gy);ctx.stroke();
  }

  // Y-axis labels
  ctx.fillStyle="#484f58";
  ctx.font="10px monospace";
  ctx.textAlign="right";
  for(var g=0;g<=4;g++){
    var gy=pad/2+gH*(1-g/4);
    ctx.fillText(formatter(max*g/4),pad-4,gy+3);
  }

  // Data line
  ctx.strokeStyle=color;
  ctx.lineWidth=2;
  ctx.lineJoin="round";
  ctx.beginPath();
  for(var i=0;i<data.length;i++){
    var x=pad+(i/(data.length-1))*gW;
    var y=pad/2+gH*(1-data[i]/max);
    if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);
  }
  ctx.stroke();

  // Fill under line
  ctx.lineTo(pad+gW,pad/2+gH);
  ctx.lineTo(pad,pad/2+gH);
  ctx.closePath();
  ctx.fillStyle=color.replace("1)","0.08)");
  ctx.fill();

  // Current value
  ctx.fillStyle=color;
  ctx.font="bold 11px monospace";
  ctx.textAlign="right";
  ctx.fillText(formatter(data[data.length-1]),W-4,14);
}

/* ---- Share detail modal ---- */
function openShareDetail(hash){
  var overlay=document.getElementById("share-modal");
  var content=document.getElementById("share-detail-content");
  content.innerHTML='<div class="loading">Loading...</div>';
  overlay.classList.add("open");
  fetch("/api/share/"+encodeURIComponent(hash))
    .then(function(r){return r.json()})
    .then(function(d){
      if(d.error){content.innerHTML='<div class="no-data">'+esc(d.error)+'</div>';return;}
      var blockBadge=d.is_block?'<span class="golden-badge">BLOCK</span>':"";
      var cls=d.is_block?' class="golden"':"";
      content.innerHTML='<dl>'+
        '<dt>Hash</dt><dd'+cls+'>'+esc(d.hash)+blockBadge+'</dd>'+
        '<dt>Miner</dt><dd>'+addrLink(d.miner)+'</dd>'+
        '<dt>Time</dt><dd>'+fmtTimestamp(d.timestamp)+'</dd>'+
        '<dt>Version</dt><dd>0x'+d.version.toString(16).padStart(8,"0")+'</dd>'+
        '<dt>Prev Block Hash</dt><dd>'+esc(d.prev_block_hash)+'</dd>'+
        '<dt>Merkle Root</dt><dd>'+esc(d.merkle_root)+'</dd>'+
        '<dt>Bits</dt><dd>0x'+d.bits.toString(16).padStart(8,"0")+'</dd>'+
        '<dt>Nonce</dt><dd>'+d.nonce+'</dd>'+
        '<dt>Prev Share Hash</dt><dd>'+esc(d.prev_share_hash)+'</dd>'+
        '<dt>Share Version</dt><dd>'+d.share_version+'</dd>'+
        '<dt>Difficulty</dt><dd>'+esc(d.difficulty)+'</dd>'+
        '</dl>';
    })
    .catch(function(){content.innerHTML='<div class="no-data">Failed to load share details</div>';});
}
function closeModal(){
  document.getElementById("share-modal").classList.remove("open");
}
document.getElementById("share-modal").addEventListener("click",function(e){
  if(e.target===this)closeModal();
});

/* ---- Main update ---- */
function update(data){
  _net=data.network||"";
  document.getElementById("shares").textContent=data.share_count;
  document.getElementById("miners-peers").textContent=data.miner_count+" | "+data.peer_count;
  document.getElementById("difficulty").textContent=fmtDiff(data.difficulty);
  document.getElementById("pool-hashrate").textContent=fmtHash(data.pool_hashrate);
  document.getElementById("local-hashrate").textContent=fmtHash(data.local_hashrate);
  document.getElementById("ttb").textContent=fmtDuration(data.est_time_to_block);
  document.getElementById("last-block").textContent=data.last_block_found_time?timeAgo(data.last_block_found_time):"-";
  document.getElementById("last-block").title=data.last_block_found_hash||"";
  document.getElementById("network").textContent=data.network||"-";
  document.getElementById("uptime").textContent=fmtUptime(data.uptime_secs);
  document.getElementById("tip-hash").textContent=truncate(data.tip_hash,20);
  document.getElementById("tip-miner").innerHTML=addrLink(data.tip_miner);
  document.getElementById("target-bits").textContent=data.target_bits||"-";
  document.getElementById("stratum-port").textContent=data.stratum_port;
  document.getElementById("p2p-port").textContent=data.p2p_port;
  document.getElementById("share-target-time").textContent=(data.share_target_time_secs||0)+"s";
  document.getElementById("pplns-window").textContent=data.pplns_window_size;

  // Recent shares table (full hashes, clickable, golden for blocks)
  var tb=document.getElementById("shares-table");
  if(!data.recent_shares||data.recent_shares.length===0){
    tb.innerHTML='<tr><td colspan="3" class="no-data">No shares yet</td></tr>';
  }else{
    var h="";
    for(var i=0;i<data.recent_shares.length;i++){
      var s=data.recent_shares[i];
      var rowCls=s.is_block?' class="golden"':"";
      var badge=s.is_block?'<span class="golden-badge">BLOCK</span>':"";
      h+='<tr'+rowCls+'><td class="hash" onclick="openShareDetail(\''+esc(s.hash)+'\')">'+esc(s.hash)+badge+'</td><td class="miner">'+addrLink(s.miner)+'</td><td class="time">'+timeAgo(s.timestamp)+'</td></tr>';
    }
    tb.innerHTML=h;
  }

  // PPLNS chart
  var chart=document.getElementById("pplns-chart");
  if(!data.miner_weights||Object.keys(data.miner_weights).length===0){
    chart.innerHTML='<div class="no-data">No data yet</div>';
  }else{
    var entries=Object.entries(data.miner_weights).sort(function(a,b){return b[1]-a[1]});
    var h="";
    for(var i=0;i<entries.length;i++){
      var addr=entries[i][0],pct=entries[i][1];
      h+='<div class="bar-row"><div class="bar-label">'+addrLink(addr)+'</div><div class="bar-track"><div class="bar-fill" style="width:'+pct.toFixed(1)+'%"></div></div><div class="bar-pct">'+pct.toFixed(1)+'%</div></div>';
    }
    chart.innerHTML=h;
  }

  // Seed graph history from server on first load, then append
  if(!histSeeded&&data.history&&data.history.length>0){
    histHashrate=[];histShares=[];
    for(var i=0;i<data.history.length;i++){
      histHashrate.push(data.history[i].ph);
      histShares.push(data.history[i].sc);
    }
    histSeeded=true;
  }else{
    pushHistory(histHashrate,data.pool_hashrate||0);
    pushHistory(histShares,data.share_count||0);
  }
  drawGraph("graph-hashrate",histHashrate,"rgba(88,166,255,1)",fmtHash);
  drawGraph("graph-shares",histShares,"rgba(63,185,80,1)",function(v){return Math.round(v).toString();});
}

function poll(){
  fetch("/api/status").then(function(r){return r.json()}).then(update).catch(function(){});
}
poll();
setInterval(poll,5000);

// Redraw graphs on resize
window.addEventListener("resize",function(){
  drawGraph("graph-hashrate",histHashrate,"rgba(88,166,255,1)",fmtHash);
  drawGraph("graph-shares",histShares,"rgba(63,185,80,1)",function(v){return Math.round(v).toString();});
});
</script>
</body>
</html>` + ""
