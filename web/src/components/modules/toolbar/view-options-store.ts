import { create } from 'zustand';
import { persist } from 'zustand/middleware';

export type ToolbarLayout = 'grid' | 'list';
export type ToolbarSortOrder = 'asc' | 'desc';
export type ToolbarSortField = 'name' | 'created';
export type ToolbarSortablePage = 'channel' | 'group';
export const TOOLBAR_PAGES = ['channel', 'group', 'model', 'log'] as const;
export type ToolbarPage = (typeof TOOLBAR_PAGES)[number];
export type LogDateRange = { start?: number; end?: number };
export type LogKeywordMode = 'default' | 'prefix' | 'exact' | 'contains';
export type LogKeywordScope = 'default' | 'content';
export type LogStatusFilter = 'all' | 'success' | 'error';

interface ToolbarViewOptionsState {
    layouts: Partial<Record<ToolbarPage, ToolbarLayout>>;
    sortFields: Partial<Record<ToolbarSortablePage, ToolbarSortField>>;
    sortOrders: Partial<Record<ToolbarPage, ToolbarSortOrder>>;
    logDateRange: LogDateRange;
    logChannelIds: number[];
    logKeywordMode: LogKeywordMode;
    logKeywordScope: LogKeywordScope;
    logStatus: LogStatusFilter;

    getLayout: (item: ToolbarPage) => ToolbarLayout;
    setLayout: (item: ToolbarPage, value: ToolbarLayout) => void;

    getSortField: (item: ToolbarSortablePage) => ToolbarSortField;
    setSortConfig: (
        item: ToolbarSortablePage,
        field: ToolbarSortField,
        order: ToolbarSortOrder
    ) => void;

    getSortOrder: (item: ToolbarPage) => ToolbarSortOrder;
    setSortOrder: (item: ToolbarPage, value: ToolbarSortOrder) => void;

    setLogDateRange: (value: LogDateRange) => void;
    setLogChannelIds: (value: number[]) => void;
    setLogKeywordMode: (value: LogKeywordMode) => void;
    setLogKeywordScope: (value: LogKeywordScope) => void;
    setLogStatus: (value: LogStatusFilter) => void;
}

export const useToolbarViewOptionsStore = create<ToolbarViewOptionsState>()(
    persist(
        (set, get) => ({
            layouts: {},
            sortFields: {},
            sortOrders: {},
            logDateRange: {},
            logChannelIds: [],
            logKeywordMode: 'default',
            logKeywordScope: 'default',
            logStatus: 'all',

            getLayout: (item) => get().layouts[item] || 'grid',
            setLayout: (item, value) => {
                set((state) => ({ layouts: { ...state.layouts, [item]: value } }));
            },

            getSortField: (item) => get().sortFields[item] === 'created' ? 'created' : 'name',
            setSortConfig: (item, field, order) => {
                set((state) => ({
                    sortFields: { ...state.sortFields, [item]: field },
                    sortOrders: { ...state.sortOrders, [item]: order },
                }));
            },

            getSortOrder: (item) => get().sortOrders[item] === 'desc' ? 'desc' : 'asc',
            setSortOrder: (item, value) => {
                set((state) => ({ sortOrders: { ...state.sortOrders, [item]: value } }));
            },

            setLogDateRange: (value) => set({ logDateRange: value }),
            setLogChannelIds: (value) => set({ logChannelIds: value }),
            setLogKeywordMode: (value) => set({ logKeywordMode: value }),
            setLogKeywordScope: (value) => set({ logKeywordScope: value }),
            setLogStatus: (value) => set({ logStatus: value }),
        }),
        {
            name: 'toolbar-view-options-storage',
            partialize: (state) => ({
                layouts: state.layouts,
                sortFields: state.sortFields,
                sortOrders: state.sortOrders,
                logDateRange: state.logDateRange,
                logChannelIds: state.logChannelIds,
                logKeywordMode: state.logKeywordMode,
                logKeywordScope: state.logKeywordScope,
                logStatus: state.logStatus,
            }),
        }
    )
);
