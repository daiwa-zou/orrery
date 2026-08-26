import { groupSegment } from '../api/client'

/**
 * The console route for an object the server has already identified.
 *
 * Every caller here has a group, version and resource that came back from a
 * server that resolved them through discovery. Building the link from those,
 * rather than pluralising a kind, is the difference between a link that works
 * for a CRD and one that 404s for exactly the objects nobody else knows how to
 * find — a CustomResourceDefinition may spell `spec.names.plural` however it
 * likes.
 */
export interface ObjectCoordinates {
  cluster: string
  group?: string
  version: string
  resource: string
  namespace?: string
  name: string
}

export function consoleHref(o: ObjectCoordinates): string {
  // "core" and "_" are the spellings the resource routes accept for the legacy
  // API group and for cluster scope; an empty segment would not route.
  return `/c/${o.cluster}/r/${groupSegment(o.group ?? '')}/${o.version}/${o.resource}/${o.namespace || '_'}/${o.name}`
}
