import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Helper penentu penutupan onInteractOutside untuk drawer/dialog (Sheet/Dialog).
//
// Latar belakang: overlay Radix di dalam drawer (dropdown Select, DropdownMenu, Popover, dll.) di-portal ke
// luar drawer. Saat overlay terbuka dan pengguna klik mask/luar drawer untuk menutupnya, satu pointerdown ini
// akan ditangani bersamaan oleh dua DismissableLayer, yaitu Select dan Sheet. Select tertutup duluan dan itu
// adalah event discrete sehingga React melakukan flush secara sinkron, akibatnya saat giliran handler Sheet,
// data-state overlay sudah berubah jadi closed, jadi mendeteksi "apakah overlay terbuka" pada saat itu juga
// secara alami tidak bisa diandalkan (sudah diverifikasi lewat pengujian).
//
// Cara yang benar: listener pointerdown milik Radix ada di fase bubbling; kita mencatat lebih dulu di fase
// capture (lebih awal darinya) apakah "saat ini ada overlay yang terbuka", lalu onInteractOutside membaca
// catatan ini untuk menentukan boleh tidaknya menutup.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture: mencatat lebih dulu sebelum handler pointerdown Radix di fase bubbling sempat jalan
  );
}

// radixOverlayWasOpenAtPointerDown mengembalikan "apakah ada overlay Radix yang terbuka saat pointerdown
// terakhir terjadi". Drawer/dialog memakainya begini: saat overlay terbuka dan mask diklik, hanya overlay yang ditutup, dirinya sendiri tidak ikut tertutup.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText menulis teks ke clipboard, mengembalikan berhasil atau tidak.
// Latar belakang: navigator.clipboard hanya tersedia di secure context (HTTPS / localhost); saat diakses
// lewat IP + HTTP nilainya undefined, dalam kondisi ini turun ke execCommand("copy").
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Lanjut ke skema fallback
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
