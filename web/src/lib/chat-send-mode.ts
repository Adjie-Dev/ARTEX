"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// Tombol kirim/baris baru untuk kotak input sesi. Preferensi murni di sisi frontend: hanya disimpan di localStorage, tidak masuk database, tidak disinkronkan antar akun,
// jadi harus diatur ulang kalau ganti browser. Lihat issue #39, versi 0.3.2 mengubah kirim dengan Ctrl+Enter menjadi kirim dengan Enter,
// di sini binding lama dikembalikan sebagai opsi pilihan.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Kirim dengan Enter, baris baru dengan Shift+Enter" },
  { value: "ctrl-enter", label: "Kirim dengan Ctrl+Enter, baris baru dengan Enter" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// Kumpulan subscriber dalam tab yang sama. Event storage pada localStorage hanya terpicu di tab "lain",
// jadi setelah diubah di halaman pengaturan, perlu emit untuk memberi tahu kotak input di halaman yang sama, kalau tidak harus refresh dulu baru berlaku.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Yang dikembalikan adalah literal string, Object.is membandingkan berdasarkan nilai, jadi useSyncExternalStore tidak akan terjebak dalam loop.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// Server tidak punya localStorage, render dulu nilai default, setelah hydrate getSnapshot baru mengoreksinya.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey menentukan apakah satu penekanan tombol harus mengirim pesan.
// isComposing / keyCode 229 berarti input method (misalnya untuk bahasa Mandarin) sedang memilih karakter, harus dibiarkan lewat, kalau tidak Enter untuk memilih kata akan salah terkirim sebagai pesan.
// Mode enter hanya mengecualikan Shift, sama persis dengan perilaku 0.3.2, supaya pengguna yang tidak mengubah pengaturan tidak merasakan perbedaan.
// Mode ctrl-enter menerima baik Ctrl maupun Cmd (macOS).
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
