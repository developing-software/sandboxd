// What every page shares: the nav, the owner id, a poll that stops with the tab hidden,
// and the one way a minted Link is opened. The owner id is the parent app's user; here it
// is whatever was typed, kept in localStorage so it survives navigation between pages.
import type { Link } from '@sandboxd/sdk'

export const $ = <T extends HTMLElement = HTMLElement>(id: string) =>
  document.querySelector(`#${id}`) as T

export const esc = (v: unknown) =>
  String(v).replaceAll(
    /[&<>"]/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] ?? c,
  )

export namespace Shell {
  export const owner = () => localStorage.getItem('owner') || 'dev'

  export const init = () => {
    document.body.insertAdjacentHTML(
      'afterbegin',
      `<nav>
        <a class="brand" href="/">sandboxd</a>
        <a href="/hosts">hosts</a>
        <a href="/sandboxes">sandboxes</a>
        <a href="/sandboxes/new">new</a>
        <span class="spacer"></span>
        <span id="error" class="msg"></span>
        <label>owner <input id="owner" value="${esc(owner())}"></label>
      </nav>`,
    )
    for (const a of document.querySelectorAll<HTMLAnchorElement>('nav a'))
      a.classList.toggle('active', a.pathname === location.pathname)
    $<HTMLInputElement>('owner').addEventListener('change', (e) => {
      localStorage.setItem('owner', (e.target as HTMLInputElement).value.trim())
      location.reload()
    })
  }

  /** The nav's error slot; empty clears it. */
  export const fail = (msg: string) => {
    $('error').textContent =
      msg === 'unauthorized'
        ? "unauthorized — the UI's SANDBOXD_SERVICE_TOKEN does not match the API's"
        : msg
  }

  /**
   * Open a capability in a new tab. The tab is opened inside the click and navigated after
   * the mint resolves: a `window.open` on the far side of an await has lost the user
   * gesture, and every browser blocks it.
   */
  export const openLink = async (mint: () => Promise<Link>) => {
    const tab = window.open('', '_blank')
    const link = await mint().catch((err: Error) => {
      tab?.close()
      throw err
    })
    if (tab) return tab.location.replace(link.url)
    window.open(link.url, '_blank')
  }

  /** Run `tick` now and every `ms` while the tab is visible. Returns a way to run it now. */
  export const poll = (tick: () => Promise<void>, ms = 3000) => {
    const run = () =>
      tick().then(
        () => fail(''),
        (e: Error) => fail(e.message),
      )
    run()
    setInterval(() => {
      if (!document.hidden) run()
    }, ms)
    return run
  }
}
