import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { V2_UI } from '@/lib/v2-ui'

export type NavItem = 'home' | 'site' | 'checkin' | 'channel' | 'group' | 'model' | 'log' | 'setting'

const NAV_ORDER: NavItem[] = (['home', 'site', 'checkin', 'channel', 'group', 'model', 'log', 'setting'] as NavItem[])
    .filter(item => !V2_UI || item !== 'site')

function validNavItem(item: unknown): NavItem {
    return NAV_ORDER.includes(item as NavItem) ? item as NavItem : 'home'
}

interface NavState {
    activeItem: NavItem
    prevItem: NavItem | null
    direction: number
    setActiveItem: (item: NavItem) => void
}

export const useNavStore = create<NavState>()(
    persist(
        (set, get) => ({
            activeItem: 'home',
            prevItem: null,
            direction: 0,
            setActiveItem: (item) => {
                item = validNavItem(item)
                const { activeItem } = get()
                const currentIndex = NAV_ORDER.indexOf(activeItem)
                const newIndex = NAV_ORDER.indexOf(item)
                const direction = newIndex > currentIndex ? 1 : -1

                set({
                    activeItem: item,
                    prevItem: activeItem,
                    direction
                })
            },
        }),
        {
            name: 'nav-storage',
            merge: (persisted, current) => {
                const saved = persisted as Partial<NavState> | undefined
                return {
                    ...current,
                    activeItem: validNavItem(saved?.activeItem),
                    prevItem: null,
                    direction: 0,
                }
            },
            partialize: state => ({ activeItem: state.activeItem }),
        }
    )
)
