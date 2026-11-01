import type { Health, Severity, NodeRole, OperationStatus, OperationType } from './api'

export const healthLabel: Record<Health, string> = {
  HEALTHY: 'Sain', DEGRADED: 'Dégradé', CRITICAL: 'Critique', MAINTENANCE: 'Maintenance', UNKNOWN: 'Inconnu',
}
export const severityLabel: Record<Severity, string> = { INFO: 'Info', WARNING: 'Avertissement', CRITICAL: 'Critique' }
export const roleLabel: Record<NodeRole, string> = {
  PRIMARY: 'Primaire', REPLICA: 'Réplique', DELAYED_REPLICA: 'Réplique différée', STANDALONE: 'Autonome',
}

const dtf = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'short', timeStyle: 'medium' })
export function date(s: string | null | undefined): string {
  return s ? dtf.format(new Date(s)) : '—'
}

export function ago(s: string | null | undefined): string {
  if (!s) return 'jamais'
  const sec = Math.round((Date.now() - new Date(s).getTime()) / 1000)
  if (sec < 60) return `il y a ${Math.max(sec, 0)} s`
  if (sec < 3600) return `il y a ${Math.floor(sec / 60)} min`
  if (sec < 86400) return `il y a ${Math.floor(sec / 3600)} h`
  return `il y a ${Math.floor(sec / 86400)} j`
}

export function duration(sec: number | null | undefined): string {
  if (sec === null || sec === undefined) return '—'
  if (sec < 120) return `${sec} s`
  if (sec < 7200) return `${Math.floor(sec / 60)} min`
  return `${Math.floor(sec / 3600)} h ${Math.floor((sec % 3600) / 60)} min`
}

export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  const u = ['o', 'Ko', 'Mo', 'Go', 'To']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

export function pct(n: number | null | undefined): string {
  return n === null || n === undefined ? '—' : `${n.toFixed(1)} %`
}

export const opTypeLabel: Record<OperationType, string> = {
  PREFLIGHT: 'Contrôle des prérequis', BACKUP_FULL: 'Sauvegarde complète', RESTORE_TEST: 'Restauration de contrôle',
  BACKUP_PRUNE: 'Purge des sauvegardes expirées', PROVISION_REPLICA: "Construction d'une réplique",
  REPLICATION_PAUSE: 'Suspension de la réplication', REPLICATION_RESUME: 'Reprise de la réplication',
  SWITCHOVER: 'Bascule planifiée du primaire', SWITCHOVER_DEMOTE: "Rétrogradation de l'ancien primaire",
  SWITCHOVER_PROMOTE: 'Promotion de la nouvelle source', SWITCHOVER_UNDO: 'Retour arrière (ancien primaire rouvert)',
  REPLICA_ATTACH: 'Rattachement à la source', FAILOVER: "Bascule d'urgence (primaire perdu)", FENCE: "Isolement de l'ancien primaire", DRILL: 'Exercice de bascule (aller-retour)',
  BINLOG_ARCHIVE: 'Archivage des journaux binaires', PITR_RESTORE: 'Restauration à un instant précis (isolée)', BINLOG_SEARCH: 'Recherche dans les journaux binaires',
  CONSISTENCY_CHECK: 'Contrôle de cohérence avec le primaire',
}
export const opStatusLabel: Record<OperationStatus, string> = {
  AWAITING_APPROVAL: 'En attente d\'approbation', QUEUED: 'En file', RUNNING: 'En cours', SUCCEEDED: 'Réussie',
  FAILED: 'Échec', INTERVENTION_REQUIRED: 'Intervention requise', CANCELLED: 'Annulée',
}
/** Classe de badge (réutilise les couleurs de santé). */
export const opStatusClass: Record<OperationStatus, string> = {
  AWAITING_APPROVAL: 'degraded', QUEUED: 'unknown', RUNNING: 'maintenance', SUCCEEDED: 'healthy',
  FAILED: 'critical', INTERVENTION_REQUIRED: 'critical', CANCELLED: 'unknown',
}
export const weekdayLabel = ['', 'lundi', 'mardi', 'mercredi', 'jeudi', 'vendredi', 'samedi', 'dimanche']
