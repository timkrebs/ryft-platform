// The site's only client JavaScript (D2 in docs/DECISIONS.md). Both scripts are inline,
// carry `data-ryft` (the postbuild keeps exactly those and removes the Next runtime), and
// scripts/csp.mjs hashes them into the Content-Security-Policy. They never read the URL.

/** In <head>, before first paint: a stored theme choice, so there's no flash. */
export const THEME_INIT = `try{var t=localStorage.getItem('ryft.theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t)}catch(e){}`;

/**
 * At the end of <body>: shows the theme switch (hidden without JS), applies a choice,
 * keeps it in localStorage (nothing else is stored), points the screenshots' <source>
 * elements at the chosen appearance, and updates the theme-color meta tags.
 */
export const THEME_SWITCH = `(function(){
var d=document,r=d.documentElement,K='ryft.theme';
function get(){try{return localStorage.getItem(K)}catch(e){return null}}
function set(v){try{v?localStorage.setItem(K,v):localStorage.removeItem(K)}catch(e){}}
function apply(v){
if(v!=='light'&&v!=='dark')v=null;
v?r.setAttribute('data-theme',v):r.removeAttribute('data-theme');
d.querySelectorAll('source[data-scheme]').forEach(function(s){s.media=v?(s.getAttribute('data-scheme')===v?'all':'not all'):s.getAttribute('data-media')});
d.querySelectorAll('meta[name="theme-color"][data-scheme]').forEach(function(m){m.media=v?(m.getAttribute('data-scheme')===v?'all':'not all'):m.getAttribute('data-media')});
d.querySelectorAll('[data-theme-option]').forEach(function(b){b.setAttribute('aria-pressed',String(b.getAttribute('data-theme-option')===(v||'system')))});
}
d.querySelectorAll('[data-theme-switch]').forEach(function(e){e.hidden=false});
d.addEventListener('click',function(e){var b=e.target.closest&&e.target.closest('[data-theme-option]');if(!b)return;var v=b.getAttribute('data-theme-option');v=v==='system'?null:v;set(v);apply(v)});
apply(get());
})();`;
