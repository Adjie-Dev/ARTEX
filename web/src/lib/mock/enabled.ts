// Saklar Mock. Variabel publik yang disuntikkan saat build (hanya bisa dibaca di browser dengan prefiks NEXT_PUBLIC_).
// Atur NEXT_PUBLIC_MOCK=1 di Vercel agar seluruh situs memakai mock, tanpa perlu backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
