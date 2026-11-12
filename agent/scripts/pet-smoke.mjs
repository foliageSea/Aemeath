// Run against an isolated -tags e2e app with AEMEATH_TEST_PET_SCENARIOS=1.
import net from 'node:net';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import path from 'node:path';
const socket=net.createConnection(process.env.MYGO_AUTOMATION || '/tmp/aemeath-pet-validation.sock');
await new Promise((resolve,reject)=>{socket.once('connect',resolve);socket.once('error',reject)});
let buffer='',id=0;const pending=new Map();
socket.on('data',chunk=>{buffer+=chunk.toString();let i;while((i=buffer.indexOf('\n'))>=0){const v=JSON.parse(buffer.slice(0,i));buffer=buffer.slice(i+1);pending.get(v.id)?.(v);pending.delete(v.id)}});
async function call(method,params={}){return await new Promise((resolve,reject)=>{const n=++id;const timer=setTimeout(()=>reject(Error('timeout: '+method)),10000);pending.set(n,v=>{clearTimeout(timer);v.error?reject(Error(JSON.stringify(v.error))):resolve(v.result)});socket.write(JSON.stringify({id:n,method,params})+'\n')})}
const dir=process.env.PET_CAPTURE_DIR || 'build/pet-validation';await fs.mkdir(dir,{recursive:true});
const windows=async()=>(await call('windows')).windows;
const initial=await windows();const pet=initial.find(w=>w.title==='爱弥斯桌宠'),bubble=initial.find(w=>w.title==='爱弥斯 · 对话气泡');assert(pet?.visible);assert(bubble && !bubble.visible);assert(!pet.focused);
const menu=async(label)=>{await call('menu',{path:['桌宠验收',label]});await call('idle',{window:pet.id,timeout:3000});await call('idle',{window:bubble.id,timeout:3000});await call('tree',{window:bubble.id})};
const capture=async(id,name)=>{const c=await call('capture',{window:id});await fs.writeFile(path.join(dir,name+'.png'),Buffer.from(c.png,'base64'));return {width:c.width,height:c.height}};
const report={initial,captures:[]};
try {
 await menu('暂停');report.captures.push({name:'idle',...await capture(pet.id,'pet-idle')});
 await call('input',{window:pet.id,events:[{type:'click',x:96,y:110}]});await call('idle',{window:pet.id,timeout:3000});report.captures.push({name:'wave',...await capture(pet.id,'pet-wave')});
 await call('input',{window:pet.id,events:[{type:'click',x:96,y:110,clicks:2}]});await call('idle',{window:pet.id,timeout:3000});report.captures.push({name:'jump',...await capture(pet.id,'pet-jump')});
 for(const phase of ['聆听','思考','合成','播报']){
  await menu(phase);const ws=await windows();assert(ws.find(w=>w.id===bubble.id).visible);assert(!ws.find(w=>w.id===pet.id).focused);assert(!ws.find(w=>w.id===bubble.id).focused);
  const tree=await call('tree',{window:bubble.id});assert(JSON.stringify(tree).includes('取消当前请求'));
  report.captures.push({name:phase,...await capture(pet.id,'pet-'+phase)});await capture(bubble.id,'bubble-'+phase);
 }
 await menu('回复');const replyTree=await call('tree',{window:bubble.id});assert(JSON.stringify(replyTree).includes('今天也一起'));await capture(bubble.id,'bubble-reply');
 await menu('长回复');await capture(bubble.id,'bubble-long');
 await menu('取消请求');
 await menu('放大');let ws=await windows();assert.equal(ws.find(w=>w.id===pet.id).bounds.width,288);assert.equal(ws.find(w=>w.id===pet.id).bounds.height,312);await capture(pet.id,'pet-large');
 await menu('错误');await capture(bubble.id,'bubble-error');
 await menu('隐藏');ws=await windows();assert(!ws.find(w=>w.id===pet.id).visible);assert(!ws.find(w=>w.id===bubble.id).visible);
 await menu('恢复');assert((await windows()).find(w=>w.id===pet.id).visible);
 await menu('穿透');await menu('恢复');
 await menu('取消置顶');await menu('正常大小');await menu('待机');await menu('断开');await menu('恢复连接');
 report.final=await windows();report.passed=true;
 await fs.writeFile(path.join(dir,'smoke.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify({passed:true,captures:report.captures,dir}));
} finally {socket.end()}
