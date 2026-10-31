import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createManifest, assertSameRelease } from './release-manifest.mjs';
const hash = c => 'sha256:' + c.repeat(64);
const input = () => ({tag:'v1.2.3',version:'1.2.3',commit:'a'.repeat(40),verifiedAt:'2026-10-31T00:00:00Z',protocolVersion:1,schemaVersion:6,architectures:Object.fromEntries(['amd64','arm64'].map((arch,i)=>[arch,{reference:'ghcr.io/pulse-foundation/pulse-vpn-node@'+hash(String(i+1)),imageId:hash(String(i+3)),version:'1.2.3',commit:'a'.repeat(40),architecture:arch}]))});
test('creates public manifest retaining separate registry digest and config ID',()=>{const m=createManifest(input());assert.equal(m.formatVersion,1);assert.notEqual(m.architectures.amd64.reference.split('@')[1],m.architectures.amd64.imageId);assert.equal(m.version,'1.2.3');});
test('requires both architectures',()=>{const x=input();delete x.architectures.arm64;assert.throws(()=>createManifest(x));});
for(const key of ['version','commit','architecture']) test('rejects image mismatch '+key,()=>{const x=input();x.architectures.amd64[key]='wrong';assert.throws(()=>createManifest(x));});
test('rejects tag mismatch and config ID used as digest',()=>{const x=input();x.tag='v1.2.4';assert.throws(()=>createManifest(x));const y=input();y.architectures.amd64.reference=y.architectures.amd64.reference.split('@')[0]+'@'+y.architectures.amd64.imageId;assert.throws(()=>createManifest(y));});
test('retry preserves identity while verification times may differ',()=>{const a=createManifest(input());const x=input();x.verifiedAt='2026-11-01T00:00:00Z';assert.doesNotThrow(()=>assertSameRelease(a,createManifest(x)));x.architectures.arm64.imageId=hash('9');assert.throws(()=>assertSameRelease(a,createManifest(x)));});

for (const version of ['01.2.3','1.2.3-01','1.2','dev','1.2.3-'+ 'a'.repeat(60)]) test('rejects invalid release '+version,()=>{const x=input();x.version=version;x.tag='v'+version;assert.throws(()=>createManifest(x));});

test('maps SemVer build metadata to an injective Docker tag', async()=>{
 const {registryTag}=await import('./release-manifest.mjs');
 assert.equal(typeof registryTag,'function');
 assert.equal(registryTag('v1.2.3+build.1'),'v1.2.3_build.1');
 assert.equal(registryTag('v1.2.3-rc.1'),'v1.2.3-rc.1');
 assert.throws(()=>registryTag('v1.2.3_build.1'));
});

test('registry tag CLI used by publication validates before network actions',async()=>{
 const {spawnSync}=await import('node:child_process');
 const run=tag=>spawnSync(process.execPath,['scripts/release-manifest.mjs','--registry-tag',tag],{encoding:'utf8'});
 assert.equal(run('v1.2.3+build.1').status,0);
 assert.equal(run('v1.2.3+build.1').stdout.trim(),'v1.2.3_build.1');
 assert.notEqual(run('v1.2.3_build.1').status,0);
});

test('staging references separate RC and stable images built from the same commit', async()=>{
 const {stagingTag}=await import('./release-manifest.mjs');
 assert.equal(typeof stagingTag,'function');
 const commit='a'.repeat(40);
 const rc=stagingTag('v1.2.3-rc.1',commit,'amd64');
 const stable=stagingTag('v1.2.3',commit,'amd64');
 assert.equal(rc,'verified-v1.2.3-rc.1-'+commit+'-amd64');
 assert.equal(stable,'verified-v1.2.3-'+commit+'-amd64');
 assert.notEqual(rc,stable);
 assert.equal(stagingTag('v1.2.3',commit,'amd64'),stable);
 assert.notEqual(stagingTag('v1.2.3','b'.repeat(40),'amd64'),stable);
 assert.notEqual(stagingTag('v1.2.3',commit,'arm64'),stable);
});

test('staging references map build metadata without collisions and fit Docker tag limits',async()=>{
 const {stagingTag}=await import('./release-manifest.mjs');
 assert.equal(typeof stagingTag,'function');
 const commit='a'.repeat(40);
 assert.equal(stagingTag('v1.2.3+build.1',commit,'arm64'),'verified-v1.2.3_build.1-'+commit+'-arm64');
 assert.notEqual(stagingTag('v1.2.3+build.1',commit,'arm64'),stagingTag('v1.2.3-build.1',commit,'arm64'));
 const longest=stagingTag('v1.2.3+'+'a'.repeat(58),commit,'amd64');
 assert.match(longest,/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/);
 assert.ok(longest.length<=128);
});

test('staging references reject invalid release tags, full commits and architectures',async()=>{
 const {stagingTag}=await import('./release-manifest.mjs');
 assert.equal(typeof stagingTag,'function');
 const commit='a'.repeat(40);
 for(const tag of ['1.2.3','v01.2.3','v1.2.3-01','v1.2.3_build.1','v1.2.3+'+'a'.repeat(59),'v1.2.3\n',null,12]) assert.throws(()=>stagingTag(tag,commit,'amd64'));
 for(const invalid of ['', 'a'.repeat(39),'a'.repeat(41),'A'.repeat(40),'g'.repeat(40),commit+'\n',null]) assert.throws(()=>stagingTag('v1.2.3',invalid,'amd64'));
 for(const arch of ['', 'x86_64','aarch64','amd64\n',null]) assert.throws(()=>stagingTag('v1.2.3',commit,arch));
});

test('staging CLI emits only a validated tag and rejects invalid identities',async()=>{
 const {spawnSync}=await import('node:child_process');
 const commit='a'.repeat(40);
 const run=(...args)=>spawnSync(process.execPath,['scripts/release-manifest.mjs','--staging-tag',...args],{encoding:'utf8'});
 const valid=run('v1.2.3+build.1',commit,'amd64');
 assert.equal(valid.status,0);
 assert.equal(valid.stdout,'verified-v1.2.3_build.1-'+commit+'-amd64\n');
 assert.equal(valid.stderr,'');
 for(const args of [['v1.2.3_build.1',commit,'amd64'],['v1.2.3','short','amd64'],['v1.2.3',commit,'x86_64'],['v1.2.3',commit]]) {
  const invalid=run(...args);
  assert.notEqual(invalid.status,0);
  assert.equal(invalid.stdout,'');
 }
});

test('the same release identity still rejects changed verified bytes',()=>{
 const published=createManifest(input());
 const changed=input();
 changed.architectures.amd64.reference='ghcr.io/pulse-foundation/pulse-vpn-node@'+hash('9');
 assert.throws(()=>assertSameRelease(published,createManifest(changed)),/published release identity conflict/);
});
