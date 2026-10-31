import { readFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const digest = /^sha256:[a-f0-9]{64}$/;
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
export function registryTag(tag) {
 const version=tag?.slice(1);
 if(typeof tag!== 'string' || !tag.startsWith('v') || version.length>64 || !semver.test(version)) throw Error('invalid release tag');
 return tag.replace('+','_');
}
export function stagingTag(tag,commit,architecture) {
 const releaseTag=registryTag(tag);
 if(typeof commit!=='string' || commit.length!==40 || !/^[a-f0-9]{40}$/.test(commit) || !['amd64','arm64'].includes(architecture)) throw Error('invalid staging identity');
 const result=`verified-${releaseTag}-${commit}-${architecture}`;
 if(result.length>128 || /[^A-Za-z0-9_.-]/.test(result)) throw Error('invalid staging tag');
 return result;
}
export function createManifest(input) {
 const {tag,version,commit,verifiedAt,protocolVersion,schemaVersion} = input;
 if (typeof version !== 'string' || version.length>64 || !semver.test(version) || tag !== 'v'+version || !/^[a-f0-9]{40}$/.test(commit) || !Number.isFinite(Date.parse(verifiedAt)) || protocolVersion!==1 || schemaVersion!==6) throw Error('invalid release identity');
 const architectures = {};
 for(const arch of ['amd64','arm64']) {
  const image=input.architectures?.[arch];
  if(!image || image.version!==version || image.commit!==commit || image.architecture!==arch || !digest.test(image.imageId) || !/^ghcr\.io\/pulse-foundation\/pulse-vpn-node@sha256:[a-f0-9]{64}$/.test(image.reference) || image.reference.split('@')[1]===image.imageId) throw Error('invalid verified image '+arch);
  architectures[arch]={reference:image.reference,imageId:image.imageId};
 }
 return {formatVersion:1,version,commit,verifiedAt,protocolVersion,schemaVersion,architectures};
}
export function assertSameRelease(existing, candidate) {
 const identity = value => JSON.stringify({...value,verifiedAt:undefined});
 if(identity(existing)!==identity(candidate)) throw Error('published release identity conflict');
}
if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href) {
 if(process.argv[2]==='--registry-tag') {
  process.stdout.write(registryTag(process.argv[3])+'\n');
  process.exit(0);
 }
 if(process.argv[2]==='--staging-tag') {
  process.stdout.write(stagingTag(...process.argv.slice(3))+'\n');
  process.exit(0);
 }
 const [input,output,existing]=process.argv.slice(2);
 const result=createManifest(JSON.parse(readFileSync(input,'utf8')));
 if(existing) assertSameRelease(JSON.parse(readFileSync(existing,'utf8')),result);
 writeFileSync(output,JSON.stringify(result,null,2)+'\n',{flag:'wx'});
}
