// Temporary development cutover for the entire v2 UI. Production activation
// follows the P5 migration; remove the legacy branches in P6.
export const V2_UI = process.env.NEXT_PUBLIC_V2_UI === '1';
