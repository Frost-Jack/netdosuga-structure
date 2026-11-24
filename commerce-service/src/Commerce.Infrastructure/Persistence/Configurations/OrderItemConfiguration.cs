using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class OrderItemConfiguration : IEntityTypeConfiguration<OrderItem>
{
    public void Configure(EntityTypeBuilder<OrderItem> builder)
    {
        builder.ToTable("order_items");
        builder.HasKey(x => new { x.OrderId, x.OfferId });
        builder.Property(x => x.OrderId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.OfferId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.VendorId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.Title).HasMaxLength(300).IsRequired();
        builder.Property(x => x.TotalCents).HasPrecision(20);
        builder.Property(x => x.DiscountCents).HasPrecision(20);
        builder.Property(x => x.CommissionCents).HasPrecision(20);
        builder.HasOne<Order>().WithMany().HasForeignKey(x => x.OrderId).OnDelete(DeleteBehavior.Cascade);
    }
}
