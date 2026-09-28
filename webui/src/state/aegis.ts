import { useEffect, useState } from "react";
import { authenticatedFetch } from "../api/client";
import { normalizeSnapshotId } from "../lib/formatting";
import { backendUrl } from "./buildcfg";

// Aegis Agent identity: where its releases, source code and docs live.
// Aegis Agent is a fork of Backrest (GPL-3.0); the attribution must stay visible.
export const productName = "Aegis Agent";

export const sourceRepoUrl = "https://github.com/LePhiAnhDev/aegis-agent";
export const releasesUrl = `${sourceRepoUrl}/releases`;
export const readmeUrl = `${sourceRepoUrl}#readme`;

/** A file in the Aegis Agent source repository, rendered by GitHub. */
export const docsUrl = (path: string) => `${sourceRepoUrl}/blob/main/${path}`;

export const upstreamName = "Backrest";
export const upstreamVersion = "1.14.1";
export const upstreamRepoUrl = "https://github.com/garethgeorge/backrest";

// ---------------------------------------------------------------- Aegis Cloud

/** A storage the server was attached to when Aegis Agent was installed. */
export interface AegisRepositoryPreset {
  name: string;
  region: string;
  uri: string;
}

/** GET /aegis/status: the connection to Aegis Cloud (see internal/aegis). */
export interface AegisStatus {
  enabled: boolean;
  state: "disabled" | "connecting" | "connected" | "retrying" | "stopped";
  reportUrl?: string;
  lastSuccessAt?: string;
  lastAttemptAt?: string;
  nextAttemptAt?: string;
  lastError?: string;
  pending: number;
  instanceId?: string;
  repositories: AegisRepositoryPreset[];
  runtime?: "docker" | "systemd" | "other";
}

/** The folder the Docker image writes restores to: ./aegis-agent/restores on the server. */
export const dockerRestoreDir = "/restores";

/**
 * The target a restore of `path` from a snapshot suggests. Next to the
 * original by default; in the Docker image the backed-up folders are mounted
 * read-only under /userdata, so it goes to /restores instead. Restoring the
 * whole snapshot suggests "" (the downloads folder of the interface).
 */
export const defaultRestoreTarget = (
  path: string,
  snapshotId: string,
  runtime: AegisStatus["runtime"],
  separator = "/",
) => {
  if (path === separator) return "";
  const suffix = "-aegis-restore-" + normalizeSnapshotId(snapshotId);
  if (runtime !== "docker") return path + suffix;
  const name = path.split(separator).filter(Boolean).pop() ?? "restore";
  return `${dockerRestoreDir}/${name}${suffix}`;
};

export const fetchAegisStatus = async (): Promise<AegisStatus | null> => {
  try {
    const base = new URL(backendUrl, window.location.href);
    const response = await authenticatedFetch(new URL("aegis/status", base));
    if (!response.ok) return null;
    return (await response.json()) as AegisStatus;
  } catch {
    return null;
  }
};

/** The Aegis Cloud connection, refreshed every `pollMs` (0: once). */
export const useAegisStatus = (pollMs = 30_000) => {
  const [status, setStatus] = useState<AegisStatus | null>(null);
  useEffect(() => {
    let alive = true;
    const load = async () => {
      const next = await fetchAegisStatus();
      if (alive) setStatus(next);
    };
    void load();
    const timer = pollMs > 0 ? setInterval(load, pollMs) : undefined;
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
    };
  }, [pollMs]);
  return status;
};

const aegisBucket =
  /^aegis-[0-9a-z]{6}-(apac|weur|eeur|wnam|enam|oc)-[0-9a-z]{6}$/;
const r2Host = /^[0-9a-f]{32}\.r2\.cloudflarestorage\.com$/;

/** s3:https://<account>.r2.cloudflarestorage.com/<aegis bucket>/<folder>: a repository on an Aegis Cloud storage. */
export const isAegisRepoUri = (uri: string) => {
  const rest = uri.trim().replace(/^s3:/, "");
  if (rest === uri.trim()) return false;
  try {
    const url = new URL(rest);
    const [bucket] = url.pathname.replace(/^\/+/, "").split("/");
    return (
      url.protocol === "https:" &&
      r2Host.test(url.host) &&
      aegisBucket.test(bucket ?? "")
    );
  } catch {
    return false;
  }
};

/**
 * restic reads the storage key from these variables. They point at the ones
 * Aegis Agent was installed with, so the configuration never holds the key.
 */
export const aegisStorageEnv = [
  "AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID}",
  "AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY}",
  "AWS_DEFAULT_REGION=${AWS_DEFAULT_REGION}",
];

/** Cloudflare R2 location hints, as Aegis Cloud names them. */
export const regionLabels: Record<string, string> = {
  apac: "Asia-Pacific",
  weur: "Western Europe",
  eeur: "Eastern Europe",
  wnam: "Western North America",
  enam: "Eastern North America",
  oc: "Oceania",
};
