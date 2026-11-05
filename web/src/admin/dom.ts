// The only way the page builds DOM. Text goes in as text nodes, attributes through
// setAttribute with a short allow-list of names; there is no HTML string anywhere, no
// `on*` attribute and no inline style (the CSP forbids them and Trusted Types is 'none').
// Everything the api or a tester wrote reaches the DOM through here.

export type Child = Node | string | number | null | undefined | false;
export type AttrValue = string | number | boolean | null | undefined;

const NAME = /^[a-z][a-z0-9-]*$/;
const FORBIDDEN = new Set(['style', 'srcdoc', 'formaction', 'action', 'xlink:href']);

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, AttrValue> = {},
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs)) {
    if (value === undefined || value === null || value === false) continue;
    if (!NAME.test(name) || name.startsWith('on') || FORBIDDEN.has(name)) {
      throw new Error(`attribute not allowed: ${name}`);
    }
    if (name === 'href' && !(typeof value === 'string' && value.startsWith('#'))) {
      throw new Error('href is only allowed for in-page links');
    }
    if (name === 'src' && !(typeof value === 'string' && value.startsWith('data:image/svg+xml'))) {
      throw new Error('src is only allowed for the QR code');
    }
    el.setAttribute(name, value === true ? '' : String(value));
  }
  append(el, children);
  return el;
}

export function append(el: Element, children: Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    el.append(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(String(c)) : c);
  }
}

export function listen<E extends Element, K extends keyof HTMLElementEventMap>(
  el: E,
  type: K,
  fn: (ev: HTMLElementEventMap[K]) => void,
): E {
  el.addEventListener(type, fn as EventListener);
  return el;
}

let counter = 0;
/** A unique id for label/aria wiring. */
export function uid(prefix: string): string {
  counter += 1;
  return `${prefix}-${counter}`;
}
