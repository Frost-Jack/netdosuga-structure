using Commerce.Domain;
using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class OrderConfiguration : IEntityTypeConfiguration<Order>
{
    public void Configure(EntityTypeBuilder<Order> builder)
    {
        builder.ToTable("orders");
        builder.HasKey(x => x.Id);
        builder.Property(x => x.Status)
            .HasConversion(v => v.ToString().ToUpperInvariant(), v => Enum.Parse<OrderStatus>(v, true))
            .HasMaxLength(16);
        builder.Property(x => x.CustomerId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.IdempotencyKey).HasMaxLength(128).IsRequired();
        builder.Property(x => x.PaymentId).HasMaxLength(128);
        builder.Property(x => x.RefundId).HasMaxLength(128);
        builder.Property(x => x.TotalCents).HasPrecision(20);
        builder.Property(x => x.DiscountCents).HasPrecision(20);
        builder.Property(x => x.CommissionCents).HasPrecision(20);
        builder.HasIndex(x => new { x.CustomerId, x.IdempotencyKey }).IsUnique();
    }
}
