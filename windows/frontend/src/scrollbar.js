// Custom overlay scrollbar: a thin capsule that lives in the container's own
// reserved right-edge padding, never over content, invisible until there's a
// reason to look at it. Replaces the OS scrollbar entirely (hidden via CSS -
// see .scroll-area in style.css) rather than just skinning it, since the
// wanted behaviour - 1200ms idle auto-hide, drag persisting past the
// container's edge, click-the-track paging - isn't expressible through
// ::-webkit-scrollbar alone.
//
// Wraps each scrollable element in a position:relative shell (.scrollbar-
// wrapper) with the thumb as an absolutely-positioned sibling, so the thumb
// never scrolls away with the content it represents.

const MIN_THUMB = 40;
const IDLE_HIDE_MS = 1200;
const TRACK_HIT_ZONE = 24; // px from the wrapper's right edge counted as "the gutter", generous enough to cover every container's own right padding
const isCoarsePointer = window.matchMedia && window.matchMedia('(pointer: coarse)').matches;
const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

function attach(el) {
  if (el.dataset.scrollbarAttached) return;
  el.dataset.scrollbarAttached = '1';
  el.classList.add('scroll-area');

  const wrapper = document.createElement('div');
  wrapper.className = 'scrollbar-wrapper';
  el.parentNode.insertBefore(wrapper, el);
  wrapper.appendChild(el);

  const thumb = document.createElement('div');
  thumb.className = 'scrollbar-thumb';
  if (isCoarsePointer) thumb.classList.add('coarse');
  wrapper.appendChild(thumb);

  let hideTimer = null;
  let hovering = false;
  let dragging = false;
  let dragStartY = 0;
  let dragStartScroll = 0;
  let thumbH = 0;

  function geometry() {
    const contentH = el.scrollHeight;
    const viewH = el.clientHeight;
    return { contentH, viewH, scrollable: contentH > viewH + 1 && viewH >= 120 };
  }

  function layout() {
    const { contentH, viewH, scrollable } = geometry();
    if (!scrollable) {
      thumb.style.display = 'none';
      return;
    }
    thumb.style.display = 'block';
    thumbH = Math.max(MIN_THUMB, (viewH * viewH) / contentH);
    const maxScroll = contentH - viewH;
    const maxOffset = viewH - thumbH;
    const offset = maxScroll > 0 ? (maxOffset * el.scrollTop) / maxScroll : 0;
    thumb.style.height = `${thumbH}px`;
    thumb.style.transform = `translateY(${offset}px)`;
  }

  function show() {
    thumb.classList.add('visible');
    if (hideTimer) { clearTimeout(hideTimer); hideTimer = null; }
  }
  function scheduleHide() {
    if (hovering || dragging) return;
    if (hideTimer) clearTimeout(hideTimer);
    hideTimer = setTimeout(() => thumb.classList.remove('visible'), IDLE_HIDE_MS);
  }

  // Resize (window resize, a reflow like the palette grid changing column
  // count) must not jump the visible content - remembered continuously as
  // "whichever direct child is nearest the top edge, and its pixel offset
  // from that edge" rather than a raw scrollTop, since scrollTop in pixels
  // stops meaning the same place once the content's own height changes.
  let anchor = null;
  function captureAnchor() {
    const elRect = el.getBoundingClientRect();
    for (const child of el.children) {
      const rect = child.getBoundingClientRect();
      if (rect.bottom > elRect.top) {
        anchor = { child, offset: rect.top - elRect.top };
        return;
      }
    }
    anchor = null;
  }
  function restoreAnchor() {
    if (!anchor || !anchor.child.isConnected) return;
    const elRect = el.getBoundingClientRect();
    const rect = anchor.child.getBoundingClientRect();
    el.scrollTop += (rect.top - elRect.top) - anchor.offset;
  }

  el.addEventListener('scroll', () => {
    layout();
    show();
    scheduleHide();
    captureAnchor();
  });

  if (!isCoarsePointer) {
    // Hovering the general area reveals the indicator (and pauses the idle
    // timer) without widening it - only hovering the thumb itself does that,
    // via the CSS :hover rule on .scrollbar-thumb.
    wrapper.addEventListener('mouseenter', () => {
      hovering = true;
      show();
    });
    wrapper.addEventListener('mouseleave', () => {
      hovering = false;
      scheduleHide();
    });

    thumb.addEventListener('mousedown', (e) => {
      dragging = true;
      thumb.classList.add('dragging');
      dragStartY = e.clientY;
      dragStartScroll = el.scrollTop;
      e.preventDefault();
      e.stopPropagation();
    });
    // On window, not the thumb/wrapper: dragging must keep tracking the
    // mouse even once the cursor leaves the scroll area entirely.
    window.addEventListener('mousemove', (e) => {
      if (!dragging) return;
      const { contentH, viewH } = geometry();
      const maxOffset = viewH - thumbH;
      const maxScroll = contentH - viewH;
      const deltaScroll = maxOffset > 0 ? ((e.clientY - dragStartY) * maxScroll) / maxOffset : 0;
      el.scrollTop = dragStartScroll + deltaScroll;
    });
    window.addEventListener('mouseup', () => {
      if (!dragging) return;
      dragging = false;
      thumb.classList.remove('dragging');
      if (!hovering) scheduleHide();
    });

    // Clicking the reserved gutter (not the thumb) pages toward that side,
    // smoothly - never a jump-to-position. Judged by click position, not
    // event target: the scrollable element itself normally covers the whole
    // wrapper box (its own padding included), so a target-based check would
    // never see a bare "clicked the wrapper" event in practice.
    wrapper.addEventListener('mousedown', (e) => {
      if (dragging) return;
      const { scrollable } = geometry();
      if (!scrollable) return;
      const wrapperRect = wrapper.getBoundingClientRect();
      if (e.clientX - wrapperRect.left < wrapperRect.width - TRACK_HIT_ZONE) return;
      const thumbRect = thumb.getBoundingClientRect();
      if (e.clientY >= thumbRect.top && e.clientY <= thumbRect.bottom) return; // thumb's own handler owns this
      const dir = e.clientY < thumbRect.top ? -1 : 1;
      el.scrollBy({ top: dir * el.clientHeight * 0.9, behavior: reduceMotion ? 'auto' : 'smooth' });
    });
  }

  // Container resize (window resize, sidebar changes) and content resize
  // (a list re-rendering with a different item count, or a descendant like
  // the palette grid changing its own column count via an inline style)
  // both change the geometry the thumb is drawn from - watched separately
  // since neither implies the other. Resize is also the one that needs
  // restoreAnchor: a mutation-driven height change (items added/removed)
  // is a real change in what's on screen, so snapping back to the
  // previous anchor there would fight the new content instead of just
  // preserving the view across a pixel-geometry change.
  new ResizeObserver(() => {
    restoreAnchor();
    layout();
  }).observe(el);
  new MutationObserver(layout).observe(el, {
    childList: true, subtree: true, attributes: true, attributeFilter: ['style', 'class'],
  });

  captureAnchor();

  // Exposed so relayoutScrollbars can force a recompute right when a screen
  // becomes visible - a ResizeObserver on a display:none element (0x0) does
  // fire again once it's shown, but this makes that not the only path.
  el._scrollbarLayout = layout;

  layout();
}

export function initScrollbars(root = document) {
  for (const el of root.querySelectorAll('.content, .config-list, .resource-list')) {
    attach(el);
  }
}

export function relayoutScrollbars(root = document) {
  for (const el of root.querySelectorAll('.scroll-area')) {
    if (el._scrollbarLayout) el._scrollbarLayout();
  }
}
