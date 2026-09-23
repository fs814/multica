import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { preflight } from './center-services.mjs';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const alive = pid => { try { process.kill(pid, 0); return true; } catch { return false; } };
async function until(fn, ms = 5000) { const end = Date.now() + ms; while (Date.now() < end) { if (fn()) return; await delay(25); } throw new Error('test deadline'); }
test('same directory lock is refused unchanged, including malformed/stale records', t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'center-lock-')); t.after(() => fs.rmSync(dir,{recursive:true,force:true}));
  fs.mkdirSync(path.join(dir,'apps/web/.next/dev'),{recursive:true}); const lock=path.join(dir,'apps/web/.next/dev/lock');
  for (const value of [JSON.stringify({pid:process.pid,port:3001}), '{}', 'invalid']) {
    fs.writeFileSync(lock,value); assert.throws(()=>preflight(dir),/Next development lock/); assert.equal(fs.readFileSync(lock,'utf8'),value);
  }
});
for (const scenario of ['web-failure','api-failure','timeout','signal']) {
  test(`supervisor ${scenario} cleans owned children and descendants, preserves unrelated process`, { timeout: 15000 }, async t => {
    const dir=fs.mkdtempSync(path.join(os.tmpdir(),'center-supervisor-')); t.after(()=>fs.rmSync(dir,{recursive:true,force:true}));
    const survivor=spawn(process.execPath,['-e','setInterval(()=>{},1000)']);
    t.after(()=>survivor.kill());
    const pids=path.join(dir,'pids');
    const child=path.join(dir,'child.mjs');
    fs.writeFileSync(child, `import fs from 'node:fs'; import {spawn} from 'node:child_process';
const role=process.argv[2];fs.appendFileSync(${JSON.stringify(pids)},process.pid+'\\n');
if(role==='API') spawn(process.execPath,['-e',${JSON.stringify(`require('fs').appendFileSync(${JSON.stringify(pids)},process.pid+String.fromCharCode(10));setInterval(()=>{},1000)`)}],{stdio:'inherit'});
setInterval(()=>{},1000);
if((${JSON.stringify(scenario)}==='web-failure'&&role==='Web')||(${JSON.stringify(scenario)}==='api-failure'&&role==='API'))setTimeout(()=>process.exit(23),700);`);
    const driver=path.join(dir,'driver.mjs');
    fs.writeFileSync(driver, `import {supervise} from ${JSON.stringify(new URL('./center-services.mjs',import.meta.url).href)};
process.exitCode=await supervise(['API','Web'].map(name=>({name,command:process.execPath,args:[${JSON.stringify(child)},name]})),{ready:async()=>${scenario!=='timeout'},startupMs:800,graceMs:500});`);
    const runner=spawn(process.execPath,[driver],{stdio:['ignore','pipe','pipe']});let output='';runner.stdout.on('data',x=>output+=x);runner.stderr.on('data',x=>output+=x);
    t.after(()=>{ if(runner.exitCode===null) runner.kill('SIGTERM'); });
    const exited=new Promise(resolve=>runner.once('exit',resolve));
    await until(()=>fs.existsSync(pids)&&fs.readFileSync(pids,'utf8').trim().split('\n').length===3);
    if(scenario==='signal') { await until(()=>output.includes('Center ready')); runner.kill('SIGTERM'); }
    assert.equal(await exited,scenario==='signal'?143:scenario==='timeout'?1:23,output);
    const owned=fs.readFileSync(pids,'utf8').trim().split('\n').map(Number);
    await until(()=>owned.every(pid=>!alive(pid)));
    assert.ok(alive(survivor.pid));
    if(scenario==='timeout') assert.match(output,/timed out/);
    else assert.match(output,/Center ready/);
  });
}

for (const buildCode of [0, 17]) {
  test(`one-shot build ${buildCode} reports its phase and gates service lifetime`, { timeout: 10000 }, async t => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'center-build-'));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const marker = path.join(dir, 'service-pid');
    const driver = path.join(dir, 'driver.mjs');
    fs.writeFileSync(driver, `import {supervise} from ${JSON.stringify(new URL('./center-services.mjs', import.meta.url).href)};
const code=await supervise([{name:'Build',command:process.execPath,args:['-e','process.exit(${buildCode})']}],{completeOnExit:true});
if(code!==0) process.exitCode=code;
else process.exitCode=await supervise([{name:'API',command:process.execPath,args:['-e',${JSON.stringify(`require('fs').writeFileSync(${JSON.stringify(marker)},String(process.pid));setInterval(()=>{},1000)`)}]}],{ready:async()=>true,graceMs:500});`);
    const runner = spawn(process.execPath, [driver], { stdio: ['ignore', 'pipe', 'pipe'] });
    let output = ''; runner.stdout.on('data', x => output += x); runner.stderr.on('data', x => output += x);
    t.after(() => { if (runner.exitCode === null) runner.kill('SIGTERM'); });
    const exited = new Promise(resolve => runner.once('exit', resolve));
    if (buildCode === 0) {
      await until(() => output.includes('Center ready:') && fs.existsSync(marker));
      const pid = Number(fs.readFileSync(marker, 'utf8'));
      await delay(700);
      assert.ok(alive(pid)); assert.equal(runner.exitCode, null);
      assert.match(output, /Build completed successfully/);
      assert.doesNotMatch(output, /stopping this launch/);
      runner.kill('SIGTERM');
      assert.equal(await exited, 143, output);
      await until(() => !alive(pid));
    } else {
      assert.equal(await exited, buildCode, output);
      assert.match(output, /Build failed \(17\); services not started/);
      assert.ok(!fs.existsSync(marker));
    }
  });
}
