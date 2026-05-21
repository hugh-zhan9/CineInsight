import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const appJsSource = readFileSync(new URL('../wailsjs/go/main/App.js', import.meta.url), 'utf8');
const appDtsSource = readFileSync(new URL('../wailsjs/go/main/App.d.ts', import.meta.url), 'utf8');
const modelsSource = readFileSync(new URL('../wailsjs/go/models.ts', import.meta.url), 'utf8');

assert.match(appJsSource, /export function AnalyzeVideoFaces\(arg1\)/, 'Wails JS binding should expose AnalyzeVideoFaces');
assert.match(appJsSource, /export function GetLocalMLRuntimeStatus\(\)/, 'Wails JS binding should expose GetLocalMLRuntimeStatus');
assert.match(appJsSource, /export function RefreshLocalMLRuntimeStatus\(\)/, 'Wails JS binding should expose RefreshLocalMLRuntimeStatus');
assert.match(appJsSource, /export function IndexAIEmbeddings\(arg1\)/, 'Wails JS binding should expose IndexAIEmbeddings');
assert.match(appJsSource, /export function IndexLocalMLEmbeddings\(arg1\)/, 'Wails JS binding should expose IndexLocalMLEmbeddings');
assert.match(appJsSource, /export function SearchVideosSmart\(arg1, arg2, arg3, arg4, arg5, arg6, arg7, arg8, arg9, arg10\)/, 'Wails JS binding should expose SearchVideosSmart');
assert.match(appDtsSource, /AnalyzeVideoFaces\(arg1:number\):Promise<services\.VideoFaceAnalysisResult>/, 'Wails d.ts should type AnalyzeVideoFaces');
assert.match(appDtsSource, /GetLocalMLRuntimeStatus\(\):Promise<services\.LocalMLRuntimeStatus>/, 'Wails d.ts should type GetLocalMLRuntimeStatus');
assert.match(appDtsSource, /RefreshLocalMLRuntimeStatus\(\):Promise<services\.LocalMLRuntimeStatus>/, 'Wails d.ts should type RefreshLocalMLRuntimeStatus');
assert.match(appDtsSource, /IndexAIEmbeddings\(arg1:number\):Promise<services\.LocalMLEmbeddingIndexResult>/, 'Wails d.ts should type IndexAIEmbeddings');
assert.match(appDtsSource, /IndexLocalMLEmbeddings\(arg1:number\):Promise<services\.LocalMLEmbeddingIndexResult>/, 'Wails d.ts should type IndexLocalMLEmbeddings');
assert.match(appDtsSource, /SearchVideosSmart\(arg1:string,arg2:Array<number>,arg3:number,arg4:number,arg5:number,arg6:number,arg7:number,arg8:number,arg9:number,arg10:number\):Promise<Array<models\.Video>>/, 'Wails d.ts should type SearchVideosSmart');
assert.match(modelsSource, /ai_backend_mode: string;/, 'Settings model should include AI backend mode');
assert.match(modelsSource, /local_ml_model: string;/, 'Settings model should include local ML model');
assert.match(modelsSource, /local_ml_device: string;/, 'Settings model should include local ML device');
assert.match(modelsSource, /ai_embedding_model: string;/, 'Settings model should include API embedding model');
assert.match(modelsSource, /search_score\??: number;/, 'Video model should include semantic search score');
assert.match(modelsSource, /export class LocalMLRuntimeStatus/, 'Wails models should include LocalMLRuntimeStatus');
assert.match(modelsSource, /device: string;/, 'LocalMLRuntimeStatus should expose selected local ML device');
assert.match(modelsSource, /export class LocalMLEmbeddingIndexResult/, 'Wails models should include LocalMLEmbeddingIndexResult');
assert.match(modelsSource, /export class VideoFaceAnalysisResult/, 'Wails models should include VideoFaceAnalysisResult');
assert.match(modelsSource, /face_count: number;/, 'VideoFaceAnalysisResult should type face_count');
assert.match(modelsSource, /cluster_count: number;/, 'VideoFaceAnalysisResult should type cluster_count');

console.log('ai-ml bindings tests passed');
