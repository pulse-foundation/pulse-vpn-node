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
