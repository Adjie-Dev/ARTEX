"use client";

import { setClientCookie } from "../cookie.client";
import { setLocalStorageValue } from "../local-storage.client";
import { PREFERENCE_PERSISTENCE, type PreferenceKey } from "./preferences-config";

export async function persistPreference(key: PreferenceKey, value: string) {
  const mode = PREFERENCE_PERSISTENCE[key];

  switch (mode) {
    case "none":
      return;

    // Pada static export tidak ada server Node, server-cookie diturunkan menjadi cookie browser (semantik penulisannya setara).
    case "server-cookie":
    case "client-cookie":
      setClientCookie(key, value);
      return;

    case "localStorage":
      setLocalStorageValue(key, value);
      return;

    default:
      return;
  }
}
