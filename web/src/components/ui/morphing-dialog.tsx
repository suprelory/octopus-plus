'use client';

import React, {
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { XIcon } from 'lucide-react';

import { cn } from '@/lib/utils';
import useClickOutside from '@/hooks/useClickOutside';
import { useIsClient } from '@/hooks/useIsClient';

const PORTAL_IGNORED_SLOTS = [
  'select-content',
  'popover-content',
  'hover-card-content',
  'dialog-content',
  'dialog-overlay',
  'alert-dialog-content',
  'alert-dialog-overlay',
] as const;

// Enter/exit are plain CSS keyframes (tw-animate-css) so they run on the
// compositor. This replaced motion's shared-layout (`layoutId`) morph, which had
// to measure trigger + panel on the main thread on every open/close — one FLIP
// participant per list row, which is what made the card and log pages janky.
//
// Drives both the CSS animation and the unmount delay for closing content. It is
// applied via inline `animationDuration` rather than a `duration-*` class because
// Tailwind cannot interpolate a constant into a class name — a class would mean
// keeping two literals in sync by hand.
const ANIMATION_DURATION_MS = 150;

const ANIMATION_DURATION_STYLE: React.CSSProperties = {
  animationDuration: `${ANIMATION_DURATION_MS}ms`,
};

// `fill-mode-forwards` on the exit keyframes matters: tw-animate-css defaults to
// `fill-mode: none`, so without it the closing node snaps back to full opacity
// for the frame between the animation ending and the unmount timer firing.
const OVERLAY_ANIMATION =
  'ease-out data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:fill-mode-forwards';

const CONTENT_ANIMATION = `${OVERLAY_ANIMATION} data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95`;

// These `data-slot` values must not collide with PORTAL_IGNORED_SLOTS: the
// click-outside guard below treats the mere presence of one of those slots in the
// document as "a higher layer is open, ignore this click". Reusing
// `dialog-content` here would make every dialog swallow its own outside-clicks.
const CONTENT_SLOT = 'morphing-dialog-content';
const BACKDROP_SLOT = 'morphing-dialog-backdrop';

export type MorphingDialogContextType = {
  isOpen: boolean;
  setIsOpen: React.Dispatch<React.SetStateAction<boolean>>;
  uniqueId: string;
  triggerRef: React.RefObject<HTMLDivElement | null>;
};

const MorphingDialogContext =
  React.createContext<MorphingDialogContextType | null>(null);

function useMorphingDialog() {
  const context = useContext(MorphingDialogContext);
  if (!context) {
    throw new Error(
      'useMorphingDialog must be used within a MorphingDialogProvider'
    );
  }
  return context;
}

function MorphingDialogProvider({
  children,
  open: controlledOpen,
  onOpenChange,
}: MorphingDialogProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const isControlled = controlledOpen !== undefined;
  const isOpen = isControlled ? (controlledOpen as boolean) : uncontrolledOpen;
  const uniqueId = useId();
  const triggerRef = useRef<HTMLDivElement>(null!);

  const setIsOpen = useCallback<React.Dispatch<React.SetStateAction<boolean>>>(
    (value) => {
      // 非受控：直接交给 setUncontrolledOpen，由 React 基于最新 state 计算，
      // 避免 useCallback 闭包捕获到陈旧的 uncontrolledOpen
      if (!isControlled) {
        setUncontrolledOpen(value);
        return;
      }
      // 受控：基于 controlledOpen 计算下一个值并通知外部
      const current = controlledOpen as boolean;
      const next =
        typeof value === 'function'
          ? (value as (prev: boolean) => boolean)(current)
          : value;
      if (next !== current) onOpenChange?.(next);
    },
    [isControlled, controlledOpen, onOpenChange]
  );

  const contextValue = useMemo(
    () => ({
      isOpen,
      setIsOpen,
      uniqueId,
      triggerRef,
    }),
    [isOpen, setIsOpen, uniqueId]
  );

  return (
    <MorphingDialogContext.Provider value={contextValue}>
      {children}
    </MorphingDialogContext.Provider>
  );
}

export type MorphingDialogProps = {
  children: React.ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
};

function MorphingDialog({ children, open, onOpenChange }: MorphingDialogProps) {
  return (
    <MorphingDialogProvider open={open} onOpenChange={onOpenChange}>
      {children}
    </MorphingDialogProvider>
  );
}

export type MorphingDialogTriggerProps = {
  children: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
  triggerRef?: React.RefObject<HTMLDivElement>;
  onClick?: (event: React.MouseEvent<HTMLDivElement>) => void;
  'aria-label'?: string;
};

function MorphingDialogTrigger({
  children,
  className,
  style,
  triggerRef: triggerRefProp,
  onClick,
  'aria-label': ariaLabel,
}: MorphingDialogTriggerProps) {
  const { setIsOpen, isOpen, uniqueId, triggerRef } = useMorphingDialog();

  const handleClick = useCallback(
    (event: React.MouseEvent<HTMLDivElement>) => {
      onClick?.(event);
      if (event.defaultPrevented) return;
      setIsOpen(!isOpen);
    },
    [isOpen, setIsOpen, onClick]
  );

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        setIsOpen(!isOpen);
      }
    },
    [isOpen, setIsOpen]
  );

  // The trigger stays a plain element in both states. It used to swap between a
  // motion trigger and a hidden placeholder to stop shared-layout from flashing
  // it back into place mid-morph; without layout animation there is nothing to
  // flash, so the node can just stay mounted and keep its focus target.
  return (
    <div
      ref={triggerRefProp ?? triggerRef}
      className={cn('relative cursor-pointer', className)}
      onClick={handleClick}
      onKeyDown={handleKeyDown}
      style={style}
      aria-haspopup="dialog"
      aria-expanded={isOpen}
      aria-controls={`motion-ui-morphing-dialog-content-${uniqueId}`}
      aria-label={ariaLabel ?? `Open dialog ${uniqueId}`}
      role="button"
      tabIndex={0}
    >
      {children}
    </div>
  );
}

/**
 * Tracks `isOpen` but stays `true` for one animation duration after it flips to
 * false, so closing content can play its exit keyframes before unmounting.
 * This is the small piece of bookkeeping AnimatePresence used to do for us.
 */
function usePresence(isOpen: boolean) {
  const [isExiting, setIsExiting] = useState(false);
  const [wasOpen, setWasOpen] = useState(isOpen);

  // Adjusting state during render (rather than in an effect) is the pattern
  // React documents for "derive state when a prop changes": it re-renders before
  // committing, so the closing node never paints in its unmounted state.
  if (wasOpen !== isOpen) {
    setWasOpen(isOpen);
    // Inside this branch `!isOpen` already implies `wasOpen` was true.
    setIsExiting(!isOpen);
  }

  useEffect(() => {
    if (!isExiting) return;
    const timer = window.setTimeout(
      () => setIsExiting(false),
      ANIMATION_DURATION_MS
    );
    return () => window.clearTimeout(timer);
  }, [isExiting]);

  return isOpen || isExiting;
}

export type MorphingDialogContentProps = {
  children: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
};

function MorphingDialogContent({
  children,
  className,
  style,
}: MorphingDialogContentProps) {
  const { setIsOpen, isOpen, uniqueId, triggerRef } = useMorphingDialog();
  const containerRef = useRef<HTMLDivElement>(null!);
  const firstFocusableElementRef = useRef<HTMLElement | null>(null);
  const lastFocusableElementRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      // Radix consumes Escape during capture and may unmount its portal before
      // this bubble listener runs. The event remains consumed after DOM removal.
      if (event.defaultPrevented || (event.key !== 'Escape' && event.key !== 'Tab')) return;
      for (const slot of PORTAL_IGNORED_SLOTS) {
        if (document.querySelector(`[data-slot="${slot}"]`)) return;
      }

      if (event.key === 'Escape') {
        event.preventDefault();
        setIsOpen(false);
        return;
      }
      if (event.key === 'Tab') {
        if (!firstFocusableElementRef.current || !lastFocusableElementRef.current) return;

        if (event.shiftKey) {
          if (document.activeElement === firstFocusableElementRef.current) {
            event.preventDefault();
            lastFocusableElementRef.current.focus();
          }
        } else {
          if (document.activeElement === lastFocusableElementRef.current) {
            event.preventDefault();
            firstFocusableElementRef.current.focus();
          }
        }
      }
    };

    document.addEventListener('keydown', handleKeyDown);

    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [isOpen, setIsOpen]);

  useEffect(() => {
    if (isOpen) {
      document.body.classList.add('overflow-hidden');
      // Defer the focusable-elements scan to the next frame: a synchronous
      // querySelectorAll + focus() on a heavy panel forces a reflow on the very
      // frame the dialog opens, which is what made the open click feel stuck.
      const rafId = window.requestAnimationFrame(() => {
        const root = containerRef.current;
        if (!root) return;
        const focusableElements = root.querySelectorAll(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        if (focusableElements && focusableElements.length > 0) {
          firstFocusableElementRef.current = focusableElements[0] as HTMLElement;
          lastFocusableElementRef.current = focusableElements[focusableElements.length - 1] as HTMLElement;
          // Preserve focus chosen inside the panel before this deferred scan.
          if (root.contains(document.activeElement)) return;
          // An upper dialog may have opened before this deferred focus scan.
          if (PORTAL_IGNORED_SLOTS.some(slot => document.querySelector(`[data-slot="${slot}"]`))) return;
          (focusableElements[0] as HTMLElement).focus();
        }
      });
      return () => {
        window.cancelAnimationFrame(rafId);
      };
    } else {
      document.body.classList.remove('overflow-hidden');
      triggerRef.current?.focus();
    }
  }, [isOpen, triggerRef]);

  useClickOutside(
    containerRef,
    () => {
      if (isOpen) {
        setIsOpen(false);
      }
    },
    (event) => {
      const target = event.target as HTMLElement | null;
      for (const slot of PORTAL_IGNORED_SLOTS) {
        const selector = `[data-slot="${slot}"]`;
        if (target?.closest(selector)) return true;
        if (document.querySelector(selector)) return true;
      }
      return false;
    }
  );

  return (
    <div
      ref={containerRef}
      data-slot={CONTENT_SLOT}
      data-state={isOpen ? 'open' : 'closed'}
      className={cn('overflow-hidden', CONTENT_ANIMATION, className)}
      style={{ ...ANIMATION_DURATION_STYLE, ...style }}
      role="dialog"
      aria-modal="true"
      id={`motion-ui-morphing-dialog-content-${uniqueId}`}
      aria-labelledby={`motion-ui-morphing-dialog-title-${uniqueId}`}
      aria-describedby={`motion-ui-morphing-dialog-description-${uniqueId}`}
    >
      {children}
    </div>
  );
}

export type MorphingDialogContainerProps = {
  children: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
};

function MorphingDialogContainer({ children }: MorphingDialogContainerProps) {
  const { isOpen, uniqueId } = useMorphingDialog();
  const isClient = useIsClient();
  const isPresent = usePresence(isOpen);

  if (!isClient || !isPresent) return null;

  return createPortal(
    <>
      <div
        key={`backdrop-${uniqueId}`}
        data-slot={BACKDROP_SLOT}
        data-state={isOpen ? 'open' : 'closed'}
        aria-hidden="true"
        className={cn(
          'fixed inset-0 h-full w-full bg-white/40 backdrop-blur-xs dark:bg-black/40 z-50',
          OVERLAY_ANIMATION
        )}
        style={ANIMATION_DURATION_STYLE}
      />
      <div className="fixed inset-0 z-50 flex items-center justify-center">
        {children}
      </div>
    </>,
    document.body
  );
}

export type MorphingDialogTitleProps = {
  children: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
};

function MorphingDialogTitle({
  children,
  className,
  style,
}: MorphingDialogTitleProps) {
  const { uniqueId } = useMorphingDialog();

  return (
    <div
      id={`motion-ui-morphing-dialog-title-${uniqueId}`}
      className={className}
      style={style}
    >
      {children}
    </div>
  );
}

export type MorphingDialogDescriptionProps = {
  children: React.ReactNode;
  className?: string;
};

function MorphingDialogDescription({
  children,
  className,
}: MorphingDialogDescriptionProps) {
  const { uniqueId } = useMorphingDialog();

  return (
    <div
      className={className}
      id={`motion-ui-morphing-dialog-description-${uniqueId}`}
    >
      {children}
    </div>
  );
}

export type MorphingDialogCloseProps = {
  children?: React.ReactNode;
  className?: string;
};

function MorphingDialogClose({
  children,
  className,
}: MorphingDialogCloseProps) {
  const { setIsOpen } = useMorphingDialog();

  const handleClose = useCallback(() => {
    setIsOpen(false);
  }, [setIsOpen]);

  return (
    <button
      onClick={handleClose}
      type="button"
      aria-label="Close dialog"
      className={cn('absolute top-6 right-6', className)}
    >
      {children || <XIcon size={24} />}
    </button>
  );
}

export {
  MorphingDialog,
  MorphingDialogTrigger,
  MorphingDialogContainer,
  MorphingDialogContent,
  MorphingDialogClose,
  MorphingDialogTitle,
  MorphingDialogDescription,
  useMorphingDialog,
};
