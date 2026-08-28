import clsx from 'clsx'
import { Fragment, type ReactNode } from 'react'
import type { BarCell, Column, Row } from '../api/types'
import { duration, ratioTone } from '../lib/format'
import { rowKey, toggleAll, toggleRow } from '../lib/selection'
import { ChevronLeftIcon, ChevronRightIcon } from './icons'
import { Age, Badge, Button, Checkbox, EmptyState, Select, StatusBadge } from './primitives'

export interface DataTableProps {
  columns: Column[]
  rows: Row[]
  /** Column key the server sorted by, so the header can show the indicator. */
  sort?: string
  order?: 'asc' | 'desc'
  onSort?: (key: string) => void
  onRowClick?: (row: Row) => void
  /** Rendered at the end of each row, typically an actions menu. */
  rowActions?: (row: Row) => ReactNode
  /**
   * Multi-select is opt-in: pass both the selected row keys (see rowKey in
   * lib/selection) and a change handler to get a checkbox column plus a
   * select-all header.
   */
  selected?: ReadonlySet<string>
  onSelectedChange?: (next: Set<string>) => void
  /** Makes label chips (columns of type "labels") clickable filter toggles. */
  onLabelClick?: (key: string, value: string) => void
  emptyTitle?: string
  emptyDescription?: ReactNode
  loading?: boolean
}

/**
 * Column priority controls what survives a narrow viewport. Priority 0 is
 * always visible; higher priorities drop out at successively wider
 * breakpoints, which is the same idea kubectl's wide output encodes.
 */
const PRIORITY_CLASS: Record<number, string> = {
  0: '',
  1: 'hidden md:table-cell',
  2: 'hidden lg:table-cell',
  3: 'hidden xl:table-cell',
}

function priorityClass(priority?: number): string {
  if (!priority) return ''
  return PRIORITY_CLASS[Math.min(priority, 3)] ?? PRIORITY_CLASS[3]
}

function Cell({
  column,
  row,
  onLabelClick,
}: {
  column: Column
  row: Row
  onLabelClick?: (key: string, value: string) => void
}) {
  const value = row[column.key]

  if (value === undefined || value === null || value === '') {
    return <span className="text-ink-faint">—</span>
  }

  switch (column.type) {
    case 'bar': {
      const { text, percent } = value as BarCell
      const fill =
        percent >= 90 ? 'bg-danger' : percent >= 75 ? 'bg-warn' : 'bg-accent'
      return (
        <span className="flex items-center justify-end gap-[7px]">
          <span className="inline-block h-1 w-[52px] border border-border bg-canvas">
            <span
              className={clsx('block h-full', fill)}
              style={{ width: `${Math.min(100, percent)}%` }}
            />
          </span>
          <span className="min-w-[50px] text-right font-mono text-xs whitespace-nowrap text-ink-muted">
            {text}
          </span>
        </span>
      )
    }

    case 'labels': {
      const labels = Object.entries(value as Record<string, string>).sort(([a], [b]) =>
        a.localeCompare(b),
      )
      if (labels.length === 0) return <span className="text-ink-faint">—</span>
      return (
        <span className="flex max-w-[28rem] flex-wrap gap-1">
          {labels.map(([k, v]) => (
            <button
              key={k}
              type="button"
              disabled={!onLabelClick}
              title={onLabelClick ? `Toggle label filter ${k}=${v}` : undefined}
              className={clsx(
                'max-w-[16rem] truncate bg-canvas px-1.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-border',
                onLabelClick && 'cursor-pointer hover:text-ink hover:ring-accent',
              )}
              onClick={
                onLabelClick
                  ? (e) => {
                      // The row itself navigates; a chip click must not.
                      e.stopPropagation()
                      onLabelClick(k, v)
                    }
                  : undefined
              }
            >
              {k}={v}
            </button>
          ))}
        </span>
      )
    }
    case 'status':
      return <StatusBadge value={String(value)} />

    case 'age':
      return <Age timestamp={String(value)} />

    case 'ratio': {
      const text = String(value)
      return <Badge tone={ratioTone(text)}>{text}</Badge>
    }

    case 'bool':
      return value ? (
        <Badge tone="info">yes</Badge>
      ) : (
        <span className="text-ink-faint">no</span>
      )

    case 'list': {
      const items = Array.isArray(value) ? value.map(String) : [String(value)]
      if (items.length === 0) return <span className="text-ink-faint">—</span>
      return (
        <span className="flex flex-wrap gap-1" title={items.join('\n')}>
          {items.slice(0, 2).map((item, i) => (
            <span
              key={`${item}-${i}`}
              className="max-w-[22rem] truncate bg-canvas px-1.5 py-0.5 font-mono text-[11px] text-ink-muted ring-1 ring-border"
            >
              {item}
            </span>
          ))}
          {items.length > 2 && (
            <span className="text-[11px] text-ink-faint">+{items.length - 2}</span>
          )}
        </span>
      )
    }

    case 'number':
      return <span className="tabular-nums">{String(value)}</span>

    default:
      // The Job duration column ships an encoded pair; everything else is a
      // plain string.
      if (column.key === 'duration') return <span>{duration(value)}</span>
      return <span className="break-words">{String(value)}</span>
  }
}

function SortIndicator({ active, order }: { active: boolean; order?: 'asc' | 'desc' }) {
  return (
    <span
      aria-hidden
      className={clsx(
        'ml-1 inline-block text-[10px] transition-opacity',
        active ? 'opacity-100' : 'opacity-0 group-hover:opacity-40',
      )}
    >
      {active && order === 'desc' ? '▼' : '▲'}
    </span>
  )
}

export function DataTable({
  columns,
  rows,
  sort,
  order,
  onSort,
  onRowClick,
  rowActions,
  selected,
  onSelectedChange,
  onLabelClick,
  emptyTitle = 'Nothing here',
  emptyDescription,
  loading,
}: DataTableProps) {
  const selectable = !!selected && !!onSelectedChange

  if (!loading && rows.length === 0) {
    return <EmptyState title={emptyTitle} description={emptyDescription} />
  }

  const keys = selectable ? rows.map(rowKey) : []
  const allSelected = keys.length > 0 && keys.every((k) => selected!.has(k))
  const someSelected = keys.some((k) => selected!.has(k))

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[40rem] border-collapse text-[13px]">
        <thead>
          <tr className="border-b border-border text-left">
            {selectable && (
              <th scope="col" className="w-8 px-3 py-2">
                <Checkbox
                  aria-label={allSelected ? 'Deselect all rows' : 'Select all rows'}
                  checked={allSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = someSelected && !allSelected
                  }}
                  onChange={() => onSelectedChange!(toggleAll(selected!, keys))}
                />
              </th>
            )}
            {columns.map((column) => {
              const active = sort === column.key
              // A labels cell holds a map; there is no meaningful order to
              // offer, so the header is inert rather than a silent no-op.
              const sortable = onSort && column.type !== 'labels' ? onSort : undefined
              return (
                <th
                  key={column.key}
                  scope="col"
                  className={clsx(
                    'group px-3 py-2 text-[11px] font-semibold tracking-[.08em] text-ink-faint uppercase',
                    column.align === 'right' && 'text-right',
                    priorityClass(column.priority),
                    sortable && 'cursor-pointer select-none hover:text-ink-muted',
                  )}
                  onClick={sortable ? () => sortable(column.key) : undefined}
                  onKeyDown={
                    sortable
                      ? (e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault()
                            sortable(column.key)
                          }
                        }
                      : undefined
                  }
                  tabIndex={sortable ? 0 : undefined}
                  aria-sort={
                    sortable
                      ? active
                        ? order === 'desc'
                          ? 'descending'
                          : 'ascending'
                        : 'none'
                      : undefined
                  }
                >
                  {column.label}
                  {sortable && <SortIndicator active={active} order={order} />}
                </th>
              )
            })}
            {rowActions && <th className="w-10 px-3 py-2" />}
          </tr>
        </thead>

        <tbody>
          {rows.map((row) => {
            const key = rowKey(row)
            const isSelected = selectable && selected!.has(key)
            return (
            <Fragment key={key}>
              <tr
                className={clsx(
                  'border-b border-ink/8 transition-colors',
                  // A row is reached with Tab and activated with Enter, so it
                  // needs a focus indicator a keyboard user can actually see.
                  // The 4% tint this used to rely on is 1.10:1 against the
                  // unfocused row — WCAG 2.4.11 asks for 3:1 — and it was
                  // identical to hover, so focus and "the mouse is over there"
                  // looked the same. The global :focus-visible outline is
                  // 3.95:1; the negative offset keeps it inside the row rather
                  // than drawn over its neighbours.
                  onRowClick &&
                    'cursor-pointer hover:bg-ink/4 focus-visible:-outline-offset-2',
                  isSelected && 'bg-accent-soft',
                  row._terminating && 'opacity-55',
                )}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                onKeyDown={
                  onRowClick
                    ? (e) => {
                        if (e.key === 'Enter' && e.target === e.currentTarget) {
                          onRowClick(row)
                        }
                      }
                    : undefined
                }
                tabIndex={onRowClick ? 0 : undefined}
              >
                {selectable && (
                  <td className="px-3 py-2 align-middle" onClick={(e) => e.stopPropagation()}>
                    <Checkbox
                      aria-label={`Select ${row.name}`}
                      checked={isSelected}
                      onChange={() => onSelectedChange!(toggleRow(selected!, key))}
                    />
                  </td>
                )}
                {columns.map((column, index) => (
                  <td
                    key={column.key}
                    className={clsx(
                      'px-3 py-2 align-middle',
                      column.align === 'right' && 'text-right tabular-nums',
                      priorityClass(column.priority),
                      index === 0 && 'font-medium text-ink',
                    )}
                  >
                    {index === 0 ? (
                      <span className="flex items-center gap-2">
                        <span className="truncate">{String(row[column.key] ?? '')}</span>
                        {row._terminating && (
                          <Badge tone="warn" title="This object is being deleted">
                            terminating
                          </Badge>
                        )}
                      </span>
                    ) : (
                      <Cell column={column} row={row} onLabelClick={onLabelClick} />
                    )}
                  </td>
                ))}
                {rowActions && (
                  <td
                    className="px-3 py-2 text-right"
                    onClick={(e) => e.stopPropagation()}
                  >
                    {rowActions(row)}
                  </td>
                )}
              </tr>
            </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/** Server-side pagination controls. */
export function Pagination({
  page,
  pageSize,
  total,
  onPage,
  onPageSize,
}: {
  page: number
  pageSize: number
  total: number
  onPage: (page: number) => void
  onPageSize: (size: number) => void
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  const first = total === 0 ? 0 : (page - 1) * pageSize + 1
  const last = Math.min(page * pageSize, total)

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-3 py-2 text-xs text-ink-faint">
      <span className="tabular-nums">
        {first.toLocaleString()}–{last.toLocaleString()} of {total.toLocaleString()}
      </span>

      <div className="flex items-center gap-3">
        <label className="flex items-center gap-1.5">
          <span>Rows</span>
          <Select
            value={pageSize}
            onChange={(e) => onPageSize(Number(e.target.value))}
            aria-label="Rows per page"
          >
            {[25, 50, 100, 250].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </label>

        <div className="flex items-center gap-1">
          <Button
            size="sm"
            icon
            variant="ghost"
            aria-label="Previous page"
            title="Previous page"
            onClick={() => onPage(page - 1)}
            disabled={page <= 1}
          >
            <ChevronLeftIcon className="size-3.5" />
          </Button>
          <span className="tabular-nums">
            {page} / {pages}
          </span>
          <Button
            size="sm"
            icon
            variant="ghost"
            aria-label="Next page"
            title="Next page"
            onClick={() => onPage(page + 1)}
            disabled={page >= pages}
          >
            <ChevronRightIcon className="size-3.5" />
          </Button>
        </div>
      </div>
    </div>
  )
}
