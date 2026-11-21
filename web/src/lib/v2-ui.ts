// Production defaults to v2. Explicit 0 is only for legacy UI regression tests;
// remove that compatibility branch in P6.
export const V2_UI = process.env.NEXT_PUBLIC_V2_UI !== '0';
