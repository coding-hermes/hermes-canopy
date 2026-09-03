import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import PluginUpdateBanner from '../components/PluginUpdateBanner';
import type { PluginManifest } from '../lib/pluginTypes';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const oldManifest: PluginManifest = { name:'viewer',version:'1.0.0',description:'old',permissions:['data_read'],render_type:'card',entry_point:'main' };
const newManifest: PluginManifest = { ...oldManifest,version:'2.0.0',permissions:['notification'],render_type:'embed',entry_point:'start' };
describe('PluginUpdateBanner',()=>{let node:HTMLDivElement;let root:Root;beforeEach(()=>{localStorage.clear();node=document.createElement('div');document.body.append(node);root=createRoot(node)});afterEach(()=>{act(()=>root.unmount());node.remove()});
it('shows installed and new versions',()=>{act(()=>root.render(<PluginUpdateBanner installed={{id:'old',name:'Viewer',manifest:oldManifest}} available={{id:'new',name:'Viewer',manifest:newManifest}}/>));expect(node.textContent).toContain('v1.0.0 → v2.0.0')});
it('persists dismiss per plugin and version',()=>{act(()=>root.render(<PluginUpdateBanner installed={{id:'old',name:'Viewer',manifest:oldManifest}} available={{id:'new',name:'Viewer',manifest:newManifest}}/>));act(()=>Array.from(node.querySelectorAll('button')).find((b)=>b.textContent==='Dismiss')!.click());expect(node.textContent).toBe('');expect(localStorage.getItem('canopy.plugin-update.dismissed.old.2.0.0')).toBe('1')});
it('lists permission delta in changelog',()=>{act(()=>root.render(<PluginUpdateBanner installed={{id:'old',name:'Viewer',manifest:oldManifest}} available={{id:'new',name:'Viewer',manifest:newManifest}}/>));act(()=>Array.from(node.querySelectorAll('button')).find((b)=>b.textContent==='Changelog')!.click());expect(node.textContent).toContain('Permissions added: notification');expect(node.textContent).toContain('Permissions removed: data_read')});
});
