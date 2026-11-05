package com.phantom.vpn

import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch

/**
 * Long-press-and-drag reordering for a LazyColumn, without a library. The
 * dragged item's top follows the finger ([dragTop], in the column's viewport
 * coordinates), and once its middle crosses into a neighbour's slot the two
 * swap places through [onMove], which reorders the list the column renders.
 *
 * The caller draws the item at [translationFor], which is worked out from
 * where the column has actually laid the item out - read at draw time, so the
 * frame in which a swap lands needs no correction and the tile never jumps,
 * whatever the heights of the tiles it passes.
 *
 * Swaps are one slot at a time and wait for the column to lay the last one
 * out before the next, so a fast drag can't skip over a tile or move twice off
 * the same stale layout.
 */
class ReorderState(
    private val listState: LazyListState,
    private val scope: CoroutineScope,
    // Whether an item may be dragged past or onto - the list can hold items
    // that aren't part of the reorderable run (a page's header tiles).
    private val isMovable: (key: Any) -> Boolean = { true },
    private val onMove: (from: Int, to: Int) -> Unit,
) {
    var draggingKey by mutableStateOf<Any?>(null)
        private set
    private var dragTop by mutableFloatStateOf(0f)

    // The index the dragged item should have after the last swap, until the
    // column's layout shows it there.
    private var expectedIndex: Int? = null

    fun start(key: Any) {
        val info = listState.layoutInfo.visibleItemsInfo.firstOrNull { it.key == key } ?: return
        draggingKey = key
        dragTop = info.offset.toFloat()
        expectedIndex = null
    }

    fun drag(dy: Float) {
        val key = draggingKey ?: return
        dragTop += dy
        val items = listState.layoutInfo.visibleItemsInfo
        val current = items.firstOrNull { it.key == key } ?: return
        expectedIndex?.let { if (current.index != it) return }
        expectedIndex = null

        val middle = dragTop + current.size / 2f
        val target = items.firstOrNull {
            it.key != key && isMovable(it.key) && middle > it.offset && middle < it.offset + it.size
        } ?: return

        // Moving the first visible item makes the column re-anchor its scroll
        // on whatever is now first, which would yank the list under the finger.
        val first = listState.firstVisibleItemIndex
        if (current.index == first || target.index == first) {
            val offset = listState.firstVisibleItemScrollOffset
            scope.launch { listState.scrollToItem(first, offset) }
        }
        onMove(current.index, target.index)
        expectedIndex = target.index
    }

    /** How far to shift the item with [key] from where the column laid it out:
     * non-zero only for the item being dragged. Reads layout state, so call it
     * inside graphicsLayer to have it tracked at draw time. */
    fun translationFor(key: Any): Float {
        if (key != draggingKey) return 0f
        val info = listState.layoutInfo.visibleItemsInfo.firstOrNull { it.key == key } ?: return 0f
        return dragTop - info.offset
    }

    fun end() {
        draggingKey = null
        expectedIndex = null
    }
}
